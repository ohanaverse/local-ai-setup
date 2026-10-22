# Modelman Retirement, Step 0 (wt Pre-work) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Land the three wt changes the modelman retirement needs first: a test that pins the wt wording modelman matches, the #259 fix (an omlx model that is loading is not a loaded one), and the #258 fix (the TUI start flow says which models a start unloaded).

**Architecture:** `localmodels.Entry` and `catalog.Row` gain a `Loading` flag beside `Running`, set only from omlx's status reading. `Running` keeps its meaning (occupies the pool, routed, stoppable), and one new predicate, `catalog.Row.Ready()`, is what the pickers and the launch path ask before handing a model to an agent. `lifecycle.start` stops treating a loading target as a no-op, so it reaches `omlxLoad`, which already waits out a load in progress, and the TUI start flow collects `lifecycle.Options.OnUnloaded` into its done message.

**Tech Stack:** Go 1.26.7 (module root `wt/`), cobra, Bubble Tea, `net/http/httptest` fakes. No new dependencies. No Python changes.

**Spec:** [docs/superpowers/specs/2026-10-06-modelman-retirement-design.md](../specs/2026-10-06-modelman-retirement-design.md), section "Step 0 — pre-work". This plan covers Step 0 only; Step 1 is in [its own plan](2026-10-06-modelman-retirement-step1-llmbench.md), is independent of this one, and every later step gets a plan when it starts. Issues: #259, #258 (`gh issue view 259 --comments`, `gh issue view 258 --comments`).

## Global Constraints

- wt Go only. No file under `modelman/` changes: modelman is frozen to bug fixes, and it must keep working after every task.
- `make test-all` (monorepo root) passes at the end of every PR group.
- `Running` stays true for a loading model: it still occupies the pool, keeps its route and is still offered by `wt stop`. (Whether that stop succeeds is omlx's answer, not wt's: omlx 0.7.0 refuses to unload a model mid-load. See PR B's introduction.)
- `Loading` is true only when the omlx pool reading reports the model `Loading && !Loaded`. The fallback pool reading has no loading signal and keeps today's behaviour.
- `wt start` on a loading model waits for the load instead of printing "already running". A loading model is a start row in the pickers, not a launch row.
- Pinned wording, exact: `model "<id>" is not running`, `unknown model "<id>"`, and the registry-redirected refusal, whole sentence, as `checkRegistryPairing` words it (it starts with `LiteLLM routes not touched`, which is the part modelman matches).
- TUI wording, exact: `omlx unloaded X to make room`.
- `docs/contracts/wt-start-cli.sample.json` does not change: no status value is added to `wt start --json`.
- mtplx and ollama behaviour does not change. Their existing test assertions stay as they are.
- Run every Go command from `wt/`, never the monorepo root.
- Every `Test*` function has a top-level `//` comment saying what it tests and why a regression matters to a user.
- Tests never touch the developer's machine: no real omlx, no real `config.yaml`, nothing under `~/.config`. Lifecycle tests use `testEnv()`; `cmd/wt` and `internal/tui` tests rely on their `TestMain` stubs.
- Before each commit: `gofmt -l .` prints nothing and `go vet ./...` passes. `gofmt -l` exits 0 even when it lists files, so the commit steps run `test -z "$(gofmt -l .)"`, which fails when it prints anything.
- Commit messages follow the repo's conventional style (`feat(wt): …`, `fix(wt): …`, `test(wt): …`, `docs(wt): …`), with the attribution trailer your session is told to add, if any.
- Work happens on a feature branch off `main`, one branch per PR group. **Pushing a branch and opening a PR need the owner's OK.** Do not push, and do not run `gh pr create`, until the owner says so.
- Read `wt/docs/internals/local-models.md`, `wt/docs/internals/tui.md` and `wt/docs/internals/testing.md` before starting.

## Review Focus

1. **The load a start joined does not end well** (omlx runs out of room, or the load never finishes). Expected: `wt start` fails with that reason, and never prints "is running". Pinned in Task 3 (`TestStartOnALoadingTargetReportsHowTheLoadEnded`, "omlx runs out of room" and "the load never finishes").
2. **Ctrl+C while waiting on a load another process began.** Expected: the wait ends at once with a cancellation and the other loaded model is untouched. Pinned in Task 3 (same test, "ctrl+c during the wait").
3. **Two registry rows (`omlx` and `omlx-6bit`) name the one model that is loading; or omlx reports a model `loaded` and `is_loading` together.** Expected: both rows are start rows; the loaded one stays a launch row. Pinned in Task 2 (`TestInventoryLoadingFlagEdges`, the flags each entry gets) and Task 4 (`TestALoadingRowIsAStartRow`, the action each flag combination gives).
4. **A keyed omlx whose key the registry does not name, while a model loads.** Expected: exactly today's behaviour — the model reads as not running and is never marked loading. Pinned in Task 2 (`TestInventoryFallbackPoolReadingNeverSaysLoading`).
5. **omlx unloads a model and then fails the load with a long message, on an 80-column terminal.** Expected: the "omlx unloaded X to make room" note is still on screen. Pinned in Task 7 (`TestUnloadedNoteSurvivesALongFailureAtEightyColumns`). The cost of that order — with real model ids the failure's reason is what gets cut — is pinned beside it (`TestUnloadedNoteWithRealModelIDsAtEightyColumns`).
6. **A start succeeds, omlx unloaded a model for it, and the launch that follows fails.** Expected: the picker comes back with the note ahead of `launch failed: …` on the status line, not only on the terminal after wt exits. Pinned in Task 7 (`TestSuccessfulStartPrintsWhatOmlxUnloadedAboveTheAgent`).

## Decisions This Plan Makes

The spec states the behaviour; these are the choices it leaves open. Each is pinned by a test named below, so a reviewer who disagrees changes one place.

| Question | Decision | Why | Pinned by |
|---|---|---|---|
| Does a start that joins a load ask before unloading? | No. `lifecycle.evictions` returns no victims for a target that is itself loading, and `wt start <id> --plan --json` answers `fits`. | The start that began the load already asked. A second start loads nothing new, and planning it again counts the target's size twice. | Task 3, `TestStartOnALoadingTargetAsksNothing` |
| Where does a loading row sort in the picker? | With the start rows (group 2). | The sort order is also the default selection. Left in group 1, a loading model sorts first on its $0 cost and becomes what a bare Enter picks. | Task 5, `TestSortRowsPutsALoadingModelWithTheStartRows` |
| #258: what happens after a *successful* start? | The note is printed on the real terminal above the agent's output (the route notes' path). If the launch that follows fails, the picker comes back and the note is also on its status line, ahead of `launch failed: …`. | After a successful start the agent normally launches at once and `proceedToLaunch` clears the status line, so there is no picker to show a status on. When the launch fails there is one, and the spec puts the note on the picker status line. The line is then printed once more when wt exits; that repeat is accepted. | Task 7, `TestSuccessfulStartPrintsWhatOmlxUnloadedAboveTheAgent` |
| #258: note before or after the failure text? | Before, joined with `; `. With long model ids the note and `failed to start <id>: ` fill an 80-column line, so the failure's reason can be cut off; the full reason is what `wt start <id>` prints on the command line. | The status line is one line cut at the terminal's width, and one of the two has to give. The unloaded model is the fact the user can find nowhere else. | Task 7, `TestUnloadedNoteSurvivesALongFailureAtEightyColumns`, `TestUnloadedNoteWithRealModelIDsAtEightyColumns` |
| #258: does the TUI say "(not predicted)"? | No. The note is the spec's sentence only, so #258's "not predicted" motivation is met by naming every unloaded model, not by marking the surprising ones. | `Options.OnUnloaded` carries the entry, not whether the plan named it, and its other caller (`wt start --json`) pins that signature. | Task 7, `TestFailedStartShowsWhatOmlxUnloaded` |
| Where does the wording pin live? | Its own file, `wt/cmd/wt/modelman_wording_test.go`. | Step 6 of the spec deletes it with modelman; a whole file is a clean `git rm`. | Task 1 |

## Every Site That Reads `Running`

Checked on this branch with two greps from `wt/`: `grep -rn '\.Running\b\|isRunning' --include='*.go' cmd internal | grep -v _test.go` for the flag, and `grep -rn 'LoadedIDs\|\.Loading\b' --include='*.go' cmd internal | grep -v _test.go` for the pool reading that sets it. This table is the #259 audit: a site not listed here was not found by either grep.

| Site | Reads | Decision |
|---|---|---|
| `internal/localmodels/pool.go:31` `LoadedIDs`, `:114` | the pool reading: loaded or mid-load ids, and the per-model `Loading` | Unchanged: the source of both flags |
| `internal/localmodels/inventory.go:367` | `p.LoadedIDs()` feeds `source.isRunning` | Unchanged: this is why `Running` stays true for a loading model |
| `internal/localmodels/served.go:30`, `:128` (`wt served`) | loaded or mid-load ids | Unchanged: `wt served` answers "what occupies the server". modelman reads it (`wt_bridge.py:245`) only to confirm a stop |
| `internal/localmodels/inventory.go:509`, `:548` | sets `Entry.Running` | Also set `Entry.Loading` (Task 2) |
| `internal/localmodels/inventory.go:526` | mlx_lm_server matched-pairing check | Unchanged: not omlx |
| `internal/lifecycle/lifecycle.go:165` `isRunning` | "start is a no-op" | **Changed**: false for a loading target (Task 3) |
| `internal/lifecycle/evictions.go:60` `runningOthers` | pool occupancy | Unchanged: a loading sibling occupies the pool. A loading *target* gets no victims (Task 3) |
| `internal/lifecycle/lifecycle.go:220` | builds re-probe victims | Unchanged |
| `internal/lifecycle/lifecycle.go:335`, `omlxpool.go:167` | `m.Loaded \|\| m.Loading` on the pool | Unchanged: "still in the pool" |
| `internal/catalog/catalog.go:101`, `:113`, `:137` | copies `Running` into the row | Also copy `Loading` (Task 4) |
| `internal/catalog/catalog.go:114`, `:152` `listed`, `:176` `MissingReason` | is there a row / why not | Unchanged: a loading model is listed |
| `internal/catalog/catalog.go:218` `Row.Action` | launch or start | **Changed**: launch needs `Ready()` (Task 4) |
| `cmd/wt/litellm.go:349` `desiredLocalEntries` | route desire | Unchanged: a loading model keeps its route (guard test, Task 4) |
| `internal/survey/stop.go:104` `stopCandidates` | offered by `wt stop` | Unchanged (guard test, Task 4) |
| `internal/tui/modelrows.go:117` `sortRows` | sort group | **Changed**: `Ready()` (Task 5) |
| `internal/tui/modeltable.go:259` | RUNNING cell | **Changed**: `load` (Task 5) |

Every caller of `Row.Action()` follows the catalog change with no edit of its own: `cmd/wt/model_cmds.go:422` (`runStart`), `cmd/wt/resolve.go:105`, `:150`, `:168` (`resolveModel`, `pickerBlockedReason`, `launchableModels`), `cmd/wt/start_json.go:63`, `cmd/wt/smoke.go:327`, `:348`, `internal/smoke/smoke.go:113`, `internal/tui/modeltable.go:265`. Task 4's tests exercise `runStart`, `runStartJSON`, `resolveModel` and the TUI Enter path.

## File Structure

| File | Change | PR | Responsibility |
|---|---|---|---|
| `wt/cmd/wt/modelman_wording_test.go` | create | A | The wording pin; deleted with modelman |
| `wt/cmd/wt/model_cmds.go` | modify | A, B | A: a comment at the two refusals. B: one help sentence |
| `wt/internal/localmodels/inventory.go` | modify | B | `Entry.Loading`, `source.isLoading` |
| `wt/internal/localmodels/served_test.go` | modify | B | Inventory tests for the flag |
| `wt/internal/lifecycle/lifecycle.go` | modify | B | `isRunning`, `isLoading`, `targetIs` |
| `wt/internal/lifecycle/evictions.go` | modify | B | No victims for a loading target |
| `wt/internal/lifecycle/omlxpool_test.go` | modify | B | Start-on-a-loading-target tests |
| `wt/internal/catalog/catalog.go` | modify | B | `Row.Loading`, `Row.Ready`, `Row.Action` |
| `wt/internal/catalog/catalog_test.go` | modify | B | Row rule test |
| `wt/cmd/wt/loading_test.go` | create | B | `wt start`, `--json`, `-M` and route desire on a loading model |
| `wt/internal/survey/stop_test.go` | modify | B | Guard: a loading model is still a stop candidate |
| `wt/internal/tui/start_flow_test.go` | modify | B | Enter on a loading row starts |
| `wt/internal/tui/modelrows.go`, `modeltable.go` | modify | B | Sort group; RUNNING cell `load` |
| `wt/internal/tui/modelrows_test.go`, `modeltable_test.go` | modify | B | Their tests |
| `wt/internal/tui/start_flow.go` | modify | C | `OnUnloaded`, `unloadedNote`, `withNote`, `finishStart` |
| `wt/internal/tui/app.go` | modify | C | `model.startNote`; `launchSelected` puts it ahead of a launch failure |
| `wt/internal/tui/start_unloaded_test.go` | create | C | The #258 tests |
| `wt/docs/internals/local-models.md`, `wt/docs/wt-start-stop.md`, `wt/CLAUDE.md` | modify | B | Loading is not loaded |
| `docs/guides/06-wt-agents-and-models.md`, `wt/docs/wt-smoke.md` | modify | B | The RUNNING column and the launch/start row rule, as users read them |
| `wt/docs/internals/tui.md`, `wt/docs/wt-start-stop.md` | modify | C | The unloaded note |
| `wt/CHANGELOG.md` | modify | B, C | One Fixed entry each |

## Branches and PRs

Three PR groups, in this order. Each is one branch off an up-to-date `main` and one PR.

| PR | Tasks | Branch | Closes |
|---|---|---|---|
| A | 1 | `test/wt-modelman-wording-pin` | nothing |
| B | 2, 3, 4, 5, 6 | `fix/259-omlx-loading-is-not-loaded` | #259 |
| C | 7 | `fix/258-tui-shows-unloaded-models` | #258 |

Task 6 is the live check of #259 and gates PR B's handoff. Task 8 is the live check of #258 and runs after PR C; it changes no file.

**Before PR A:** the spec is committed only on `docs/modelman-retirement-design`, and this plan is not committed at all, so neither is on `main`. Commit this plan on `docs/modelman-retirement-design` and merge that branch (spec and plan) to `main`, with the owner's OK for the commit, the push and the merge. Until it has merged, a branch cut from `main` has neither file; read the spec with:

```bash
git show docs/modelman-retirement-design:docs/superpowers/specs/2026-10-06-modelman-retirement-design.md
```

Start each branch after the previous PR has merged, from the remote's `main`. This form also works inside a git worktree, where `git switch main` fails because `main` is checked out elsewhere:

```bash
git fetch origin
git switch -c <branch> origin/main
```

If the owner wants B and C open at once, they touch disjoint Go files; only `wt/CHANGELOG.md` and `wt/docs/wt-start-stop.md` need a hand merge.

Line numbers below are as of `main` at `2f1d434`. Within PR B they drift as earlier tasks land; each step also names the function or quotes the text, and that is what to match on.

---

## PR A — the wording pin

### Task 1: Pin the refusal wording modelman matches

modelman decides what a failed `wt` call means by matching wt's message, in three places:

- `modelman/src/modelman/local_control.py:1499`: `_WT_NOT_RUNNING = ("is not running", "unknown model")`, tested with `in` against the message of a failed `wt stop <id> --yes` (`_stop_unflagged_omlx`, line 1553, and `_stop_omlx_via_wt`, line 1594).
- `modelman/src/modelman/wt_bridge.py:42`: `_REGISTRY_REDIRECTED = "LiteLLM routes not touched"`, tested with `startswith` against the message of a failed `wt litellm sync --json` (`_change`, line 151).

The Go sources of those strings are `wt/cmd/wt/model_cmds.go:131` and `:133` (`runStop`) and `wt/internal/litellm/configfile.go:36` and `:79-80` (`ErrRegistryRedirected`, `checkRegistryPairing`). Existing tests check them only with `strings.Contains` (`TestStopInvalidArgErrors`) or `errors.Is` (`TestRouteWritesRefuseARedirectedRegistry`), so a reworded message passes wt-ci and breaks modelman. #267 (a status contract for `wt stop --json`) is left unfixed by the spec, so the text is the contract until modelman is deleted.

This task adds a test of existing behaviour, so it passes on its first run. Step 3 proves it can fail.

**Files:**
- Create: `wt/cmd/wt/modelman_wording_test.go`
- Modify: `wt/cmd/wt/model_cmds.go:129-134` (comment only)

**Interfaces:**
- Consumes (all existing, package `main` in `wt/cmd/wt`):
  - `func runStop(out io.Writer, cfg *config.Config, arg string, yes bool) error` (`model_cmds.go:101`)
  - `func runLitellmSync(out, errOut io.Writer, cfg *config.Config, asJSON, dryRun bool) error` (`litellm.go:130`)
  - `func modelCmdConfig() *config.Config` (`model_cmds_test.go:22`): providers `ollama`, `omlx`; models `ollama/a:1`, `ollama/b:1`, `omlx/c`
  - `func cand(provider, id, name string, sessions int) survey.Candidate` (`model_cmds_test.go:37`)
  - `func stubStop(t *testing.T, cands []survey.Candidate) *[]localmodels.Entry` (`model_cmds_test.go:46`)
  - `func stubProbeInventory(t *testing.T, snap localmodels.Snapshot)` (`testmain_test.go:79`)
- Produces: `TestRefusalsKeepTheWordingModelmanMatches`. Nothing else depends on it. Step 6 of the spec deletes the file.

- [ ] **Step 1: Create the branch**

Run from the monorepo root:

```bash
git fetch origin
git switch -c test/wt-modelman-wording-pin origin/main
```

- [ ] **Step 2: Write the test**

Create `wt/cmd/wt/modelman_wording_test.go`:

```go
package main

import (
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/ohanaverse/local-ai-setup/wt/internal/localmodels"
	"github.com/ohanaverse/local-ai-setup/wt/internal/survey"
)

// TestRefusalsKeepTheWordingModelmanMatches pins the exact text of the three
// refusals modelman decides on by matching wt's stderr, for as long as
// modelman exists (delete this file with it; see the modelman retirement
// design, Step 6):
//
//   - `wt stop <id>` for a registry model that is not running, and for an id
//     with no registry row. modelman/src/modelman/local_control.py looks for
//     the substrings "is not running" and "unknown model" (_WT_NOT_RUNNING) to
//     tell "nothing to stop" from a failed stop. Reworded, every `modelman
//     stop` of an omlx model that is already unloaded fails with wt's message
//     instead of clearing the model's running flag.
//   - the registry-redirected refusal of a route write. modelman's
//     wt_bridge.py tests whether wt's message starts with "LiteLLM routes not
//     touched" (_REGISTRY_REDIRECTED). Reworded, modelman reports a scratch
//     registry as a failed sync and tells the user to re-run it.
//
// There is no status contract for these (#267 is left unfixed), so the text is
// the contract. modelman strips a leading `wt: ` or `Error: ` from each stderr
// line before matching (wt_bridge.py, _msg), so the `wt:` prefix main puts in
// front of a returned error (main.go) is part of the contract too; this test
// sees the error before that prefix is added.
func TestRefusalsKeepTheWordingModelmanMatches(t *testing.T) {
	t.Run("wt stop, registered and not running", func(t *testing.T) {
		stubStop(t, []survey.Candidate{cand("ollama", "ollama/a:1", "a:1", 0)})
		err := runStop(io.Discard, modelCmdConfig(), "ollama/b:1", true)
		if want := `model "ollama/b:1" is not running`; err == nil || err.Error() != want {
			t.Errorf("err = %v, want exactly %q", err, want)
		}
	})
	t.Run("wt stop, no registry row", func(t *testing.T) {
		stubStop(t, []survey.Candidate{cand("ollama", "ollama/a:1", "a:1", 0)})
		err := runStop(io.Discard, modelCmdConfig(), "ollama/nope:9", true)
		if want := `unknown model "ollama/nope:9"`; err == nil || err.Error() != want {
			t.Errorf("err = %v, want exactly %q", err, want)
		}
	})
	t.Run("route write under a redirected registry", func(t *testing.T) {
		// The combination the refusal exists for: the registry is redirected
		// and nothing names config.yaml. HOME is a temp directory, so the
		// default config.yaml it resolves to is not the developer's; the
		// file has to exist, because a missing one is refused earlier, as
		// "LiteLLM config not found".
		home := t.TempDir()
		registry := filepath.Join(home, "scratch", "registry.toml")
		yaml := filepath.Join(home, ".config", "litellm", "config.yaml")
		if err := os.MkdirAll(filepath.Dir(yaml), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(yaml, []byte("model_list: []\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		t.Setenv("HOME", home)
		t.Setenv("XDG_CONFIG_HOME", "")
		t.Setenv("MODELMAN_REGISTRY", registry)
		t.Setenv("WT_LITELLM_CONFIG", "")
		t.Setenv("MODELMAN_LITELLM_CONFIG", "")
		stubProbeInventory(t, localmodels.Snapshot{})
		err := runLitellmSync(io.Discard, io.Discard, modelCmdConfig(), true, false)
		want := "LiteLLM routes not touched: the registry is " + registry +
			" but config.yaml is the default " + yaml +
			" — set WT_LITELLM_CONFIG to the config.yaml that registry belongs to"
		if err == nil || err.Error() != want {
			t.Errorf("err = %v\nwant exactly %q", err, want)
		}
	})
}
```

The third subtest writes a `config.yaml` under its temporary `HOME` because `litellm.applyPlanned` opens the file before it checks the pairing (`internal/litellm/service.go:395-404`): without the file the refusal is `LiteLLM config not found`, which is a different error.

- [ ] **Step 3: Run it, then prove it can fail**

Run: `go test ./cmd/wt -run TestRefusalsKeepTheWordingModelmanMatches -v`
Expected: PASS, three subtests.

Now reword one refusal by hand. In `cmd/wt/model_cmds.go` line 131, change `is not running` to `is not loaded`, and run the same command.
Expected: FAIL with

```
err = model "ollama/b:1" is not loaded, want exactly "model \"ollama/b:1\" is not running"
```

Put the line back: `git checkout cmd/wt/model_cmds.go`. Run the command once more.
Expected: PASS.

- [ ] **Step 4: Point the source at the test**

In `cmd/wt/model_cmds.go`, in `runStop`, replace:

```go
			if config.IndexModelByID(cfg.Models, arg) >= 0 {
				return fmt.Errorf("model %q is not running", arg)
```

with:

```go
			// modelman matches the text of these two refusals: reword them
			// only together with it (TestRefusalsKeepTheWordingModelmanMatches).
			if config.IndexModelByID(cfg.Models, arg) >= 0 {
				return fmt.Errorf("model %q is not running", arg)
```

`internal/litellm/configfile.go:32-35` already carries the same warning for `ErrRegistryRedirected`; leave it.

- [ ] **Step 5: Verify the package and the tree**

Run from `wt/`:

```bash
gofmt -l .
go build ./...
go vet ./...
go test ./...
make check
```

Expected: `gofmt -l .` prints nothing; every `go test` line starts with `ok`; `make check` ends with the Go format check passing.

- [ ] **Step 6: Commit**

```bash
git add cmd/wt/modelman_wording_test.go cmd/wt/model_cmds.go
git commit -m "test(wt): pin the refusal wording modelman matches"
```

- [ ] **Step 7: Verify as CI does, then hand off**

Run from the monorepo root: `make test-all`
Expected: lint passes, the modelman suite passes, and the wt `go test -count=1 ./...` lines all start with `ok`.

Stop here. Tell the owner the branch is ready and what `make test-all` printed. Push and open the PR only after their OK. Suggested title: `test(wt): pin the refusal wording modelman matches`. The body says: no behaviour change; the test is deleted with modelman (retirement Step 6); #267 stays open.

---

## PR B — #259, loading is not loaded

`localmodels.OmlxPool` already reads `loaded` and `is_loading` per model (`pool.go:11-18`, `:92-100`), and `Pool.LoadedIDs()` returns both kinds, so the inventory marks a model that is mid-load as `Running`. Two things then go wrong: `lifecycle.start` returns at once for a running target (`lifecycle.go:270`), so `omlxLoad` — which polls a 409 until the model is loaded (`omlxpool.go:121`) — is never reached; and `catalog.Row.Action` makes it a launch row (`catalog.go:218`).

The fix keeps `Running` and adds `Loading`. The engine changes first (Task 3), so that when the catalog starts sending loading rows to it (Task 4) it already waits.

Two facts about the real server, read from omlx 0.7.0's source (`omlx/server.py`, `omlx/engine_pool.py` under the Homebrew install) and checked live in Task 6:

- **A second load of a model that is loading.** omlx holds its pool lock for the whole load, so the second `POST /v1/models/{id}/load` normally blocks and answers 200 when the model is loaded. `omlxLoad` waits either way: it blocks on the request, and it polls a 409 or 503. The fake-server tests script the 409 path because that is the one with wt logic in it.
- **An unload of a model that is loading.** omlx answers `400 Model not loaded`, because the model has no engine yet. `omlxUnload` accepts a 400, re-reads the pool, finds the model still `is_loading` and returns `omlx still has <id> loaded` (`omlxpool.go:162-169`). So `wt stop <loading model>` fails and the load goes on; only `wt stop omlx`, which halts the service, calls a load off. That is existing behaviour and this plan does not change it. The plan's wording therefore says a loading model is *still offered by* `wt stop`, never that it *can be stopped*. The spec's Step 0 sentence "keeps its route and can be stopped" has the same error; tell the owner.

Create the branch from the monorepo root:

```bash
git fetch origin
git switch -c fix/259-omlx-loading-is-not-loaded origin/main
```

### Task 2: The inventory tells loading from loaded

**Files:**
- Modify: `wt/internal/localmodels/inventory.go:54-74` (`Entry`), `:194-223` (beside `source.isRunning`), `:505-510` and `:543-550` (`inventory`)
- Test: `wt/internal/localmodels/served_test.go` (append)

**Interfaces:**
- Consumes (existing, package `localmodels`):
  - `type PoolModel struct { ID string; Loaded, Loading, Pinned bool; LastAccess float64; Size int64 }` (`pool.go:11`)
  - `func (p Pool) Find(name string) (PoolModel, bool)` (`pool.go:47`): exact id first, then `NameMatches`
  - `source.pool *Pool` (`inventory.go:173`): omlx only, nil when the pool gave no reading
  - test helpers `fakeOmlx` with its `loading string` field (`served_test.go:17`), `localProvider`, `mkOmlxModels`, `byModelID` (`inventory_test.go:60-92`), `testClient` (`inventory_test.go:20`)
- Produces:
  - `Entry.Loading bool`: true only when `Entry.Running` is true and the omlx pool lists the model `Loading && !Loaded`
  - `func (s *source) isLoading(name string) bool`

- [ ] **Step 1: Write the failing tests**

Append to `wt/internal/localmodels/served_test.go`:

```go
// TestInventoryMarksAnOmlxModelMidLoad pins #259 at its source. omlx counts a
// model that is still loading as occupying the pool, and the inventory read
// that as plain Running: `wt start` on it printed "already running" and
// returned, and a launch handed an agent a model that could not answer yet.
// The entry now says Loading as well, for a registered model and a discovered
// one alike, and only for the model that is mid-load.
func TestInventoryMarksAnOmlxModelMidLoad(t *testing.T) {
	dir := t.TempDir()
	mkOmlxModels(t, dir, "A", "B", "C")
	// A and B are registered; C is on disk only, so its entry is discovered.
	models := []config.Model{{ID: "omlx/A", ProviderID: "omlx", ModelName: "A"}, {ID: "omlx/B", ProviderID: "omlx", ModelName: "B"}}
	for _, loading := range []string{"A", "C"} {
		t.Run(loading, func(t *testing.T) {
			srv := &fakeOmlx{listed: []string{"A", "B", "C"}, pool: map[string]bool{"A": false, "B": true, "C": false}, loading: loading}
			snap := inventory(&config.Config{Providers: []config.Provider{localProvider("omlx", srv.serve(t), dir)}, Models: models}, testClient)
			for _, id := range []string{"omlx/A", "omlx/B", "omlx/C"} {
				en, ok := byModelID(snap, id)
				if !ok {
					t.Fatalf("no entry for %s", id)
				}
				wantLoading := id == "omlx/"+loading
				wantRunning := wantLoading || id == "omlx/B"
				if en.Running != wantRunning || en.Loading != wantLoading {
					t.Errorf("%s: Running=%v Loading=%v, want %v/%v", id, en.Running, en.Loading, wantRunning, wantLoading)
				}
			}
		})
	}
}

// TestInventoryFallbackPoolReadingNeverSaysLoading pins the limit of #259's
// fix: a keyed omlx whose key the registry does not name answers only through
// /health and the list, which cannot see a model mid-load. Such a model reads
// as not running, as it did before, and is never marked Loading — a flag wt
// invented there would make `wt start` wait on a load it cannot observe.
func TestInventoryFallbackPoolReadingNeverSaysLoading(t *testing.T) {
	dir := t.TempDir()
	mkOmlxModels(t, dir, "A")
	srv := &fakeOmlx{listed: []string{"A"}, pool: map[string]bool{"A": false}, loading: "A", key: "sk-omlx"}
	cfg := &config.Config{
		Providers: []config.Provider{localProvider("omlx", srv.serve(t), dir)},
		Models:    []config.Model{{ID: "omlx/A", ProviderID: "omlx", ModelName: "A"}},
	}
	snap := inventory(cfg, testClient)
	en, ok := byModelID(snap, "omlx/A")
	if !ok || en.Running || en.Loading {
		t.Errorf("omlx/A = %+v ok=%v, want an entry that is neither running nor loading", en, ok)
	}
	if snap.OmlxPool == nil || snap.OmlxPool.SizesKnown {
		t.Errorf("pool = %+v, want the fallback reading (no sizes)", snap.OmlxPool)
	}
}

// TestInventoryLoadingFlagEdges covers two readings the plain mid-load case
// does not. omlx can report a model loaded and is_loading at once (a reload):
// it can answer, so it is running and not Loading, and stays a launch row. And
// two registry rows, omlx and omlx-6bit, can name the one model that is
// mid-load: both entries must say Loading, or the row that does not would be
// offered for launch while the other is offered for start.
func TestInventoryLoadingFlagEdges(t *testing.T) {
	dir := t.TempDir()
	mkOmlxModels(t, dir, "A")
	run := func(f *fakeOmlx, models ...config.Model) Snapshot {
		url := f.serve(t)
		return inventory(&config.Config{
			Providers: []config.Provider{localProvider("omlx", url, dir), localProvider("omlx-6bit", url, dir)},
			Models:    models,
		}, testClient)
	}
	plain := config.Model{ID: "omlx/A", ProviderID: "omlx", ModelName: "A"}
	sixBit := config.Model{ID: "omlx-6bit/A", ProviderID: "omlx-6bit", ModelName: "A"}

	snap := run(&fakeOmlx{listed: []string{"A"}, pool: map[string]bool{"A": true}, loading: "A"}, plain)
	if en, _ := byModelID(snap, "omlx/A"); !en.Running || en.Loading {
		t.Errorf("loaded and is_loading: Running=%v Loading=%v, want running and not loading", en.Running, en.Loading)
	}

	snap = run(&fakeOmlx{listed: []string{"A"}, pool: map[string]bool{"A": false}, loading: "A"}, plain, sixBit)
	for _, id := range []string{"omlx/A", "omlx-6bit/A"} {
		if en, ok := byModelID(snap, id); !ok || !en.Running || !en.Loading {
			t.Errorf("%s = %+v ok=%v, want running and loading", id, en, ok)
		}
	}
}
```

- [ ] **Step 2: Run them and confirm they fail**

Run: `go test ./internal/localmodels -run 'TestInventoryMarksAnOmlxModelMidLoad|TestInventoryFallbackPoolReadingNeverSaysLoading|TestInventoryLoadingFlagEdges' -v`
Expected: build failure, `en.Loading undefined (type Entry has no field or method Loading)`.

- [ ] **Step 3: Add the field**

In `inventory.go`, in `type Entry struct`, replace:

```go
	Running    bool // serving right now (live probe only)
```

with:

```go
	Running    bool // serving right now (live probe only)
	// Loading: omlx reports the model mid-load, not loaded yet (#259). Running
	// is true as well — a loading model occupies the pool, keeps its route and
	// is still offered by `wt stop` — but it cannot answer a request yet, so a
	// start waits for it and the pickers do not offer it for launch. Only
	// omlx's status reading can say so: it is false for every other family, and
	// for an omlx that answered through the fallback reading (Pool.SizesKnown
	// false).
	Loading bool
```

- [ ] **Step 4: Add `isLoading`**

In `inventory.go`, directly after `func (s *source) isRunning(name string) bool { … }` (it ends at line 223) and before the `// familyOrigin is the probe origin` comment, insert:

```go
// isLoading reports whether the model name denotes is mid-load: the omlx pool
// lists it loading and not loaded. It asks Pool.Find, so the name resolves to
// the same pool model a load or unload of it would act on. The fallback pool
// reading marks every model it names loaded, and no other family has a pool,
// so both answer false.
func (s *source) isLoading(name string) bool {
	if s.pool == nil {
		return false
	}
	m, ok := s.pool.Find(name)
	return ok && m.Loading && !m.Loaded
}
```

- [ ] **Step 5: Set the flag on both kinds of entry**

In `func inventory`, in the loop over `cfg.Models`, replace:

```go
			e.Running = src.isRunning(name)
```

with:

```go
			e.Running = src.isRunning(name)
			e.Loading = e.Running && src.isLoading(name)
```

In the same function, in the loop that appends discovered entries, replace:

```go
			snap.Entries = append(snap.Entries, Entry{
				ProviderID:    familyProviderID(cfg, f),
				Artifact:      a,
				ModelID:       config.DiscoveredModelID(f, a),
				ModelName:     a,
				Running:       src.isRunning(a),
				ArtifactKnown: true,
			})
```

with:

```go
			running := src.isRunning(a)
			snap.Entries = append(snap.Entries, Entry{
				ProviderID:    familyProviderID(cfg, f),
				Artifact:      a,
				ModelID:       config.DiscoveredModelID(f, a),
				ModelName:     a,
				Running:       running,
				Loading:       running && src.isLoading(a),
				ArtifactKnown: true,
			})
```

- [ ] **Step 6: Run the tests and confirm they pass**

Run: `go test ./internal/localmodels -v -run 'TestInventoryMarksAnOmlxModelMidLoad|TestInventoryFallbackPoolReadingNeverSaysLoading|TestInventoryLoadingFlagEdges'`
Expected: PASS.

Run: `go test ./...`
Expected: every line starts with `ok`. Nothing reads the new field yet, so no behaviour has changed.

- [ ] **Step 7: Commit**

```bash
test -z "$(gofmt -l .)" && go vet ./...
git add internal/localmodels
git commit -m "feat(wt): the inventory tells an omlx model that is loading from a loaded one (#259)"
```

---

### Task 3: A start on a loading target waits for the load

**Files:**
- Modify: `wt/internal/lifecycle/lifecycle.go:164-175` (`isRunning`), `:228-239` (`Start`'s comment)
- Modify: `wt/internal/lifecycle/evictions.go:17-23` (`Evictions`'s comment), `:43-46` (`evictions`)
- Test: `wt/internal/lifecycle/omlxpool_test.go` (append)

**Interfaces:**
- Consumes:
  - `localmodels.Entry.Loading bool` (Task 2)
  - existing, package `lifecycle`: `func start(ctx context.Context, e *env, cfg *config.Config, t Target, opts Options) error` (`lifecycle.go:258`), `func Evictions(t Target, snap localmodels.Snapshot) (victims []localmodels.Entry, known bool)` (`evictions.go:23`), `func omlxLoad(ctx context.Context, e *env, cfg *config.Config, modelName string) error` (`omlxpool.go:96`), `func ProbeTrusted(snap localmodels.Snapshot, family string) bool`, `func SameModel(family, a, b string) bool`
  - test helpers in `omlxpool_test.go`: `fakePool` (`loaded`, `sizes`, `ceiling`, `loadCodes`, `evictOnLoad`, `loads`, `isLoaded`), `poolEnv(t, snap) (*env, *[]string)`, `poolSnap(ceiling int64, sizes map[string]int64, loaded ...string) localmodels.Snapshot`, `captureRoutes(t)`; `provCfg(id, baseURL string) *config.Config` (`backends_test.go:21`)
- Produces:
  - `func isRunning(snap localmodels.Snapshot, family string, t Target) bool`: now false for a loading target
  - `func isLoading(snap localmodels.Snapshot, family string, t Target) bool`
  - `func targetIs(snap localmodels.Snapshot, family string, t Target, is func(localmodels.Entry) bool) bool`
  - `Evictions` returns `(nil, true)` for a target that is loading; `wt start <id> --plan --json` (Task 4) relies on that
  - test helper `func loadingSnap(snap localmodels.Snapshot, id string) localmodels.Snapshot`

- [ ] **Step 1: Write the failing tests**

Append to `wt/internal/lifecycle/omlxpool_test.go`:

```go
// loadingSnap marks one model of a poolSnap as mid-load, the way the inventory
// reports it: the pool lists it loading and not loaded, and its entry is
// Running (it occupies the pool) and Loading.
func loadingSnap(snap localmodels.Snapshot, id string) localmodels.Snapshot {
	for i := range snap.OmlxPool.Models {
		if snap.OmlxPool.Models[i].ID == id {
			snap.OmlxPool.Models[i].Loading = true
		}
	}
	for i := range snap.Entries {
		if snap.Entries[i].ModelName == id {
			snap.Entries[i].Running, snap.Entries[i].Loading = true, true
		}
	}
	return snap
}

// TestStartOnALoadingTargetWaitsForTheLoad pins #259. A model omlx is still
// loading reads as Running, and start returned at once for a running target:
// a second `wt start` printed "already running" while the model could not
// answer, and a launch handed it to an agent. The start now reaches omlxLoad,
// whose 409 answers are waited out until the model is loaded. Nothing else is
// touched: the sibling stays loaded and no route is removed.
func TestStartOnALoadingTargetWaitsForTheLoad(t *testing.T) {
	sizes := map[string]int64{"A": 20, "B": 20}
	fp := &fakePool{loaded: map[string]bool{"A": true, "B": false}, sizes: sizes, ceiling: 100, loadCodes: []int{http.StatusConflict, http.StatusConflict}}
	e, stopped := poolEnv(t, loadingSnap(poolSnap(100, sizes, "A"), "B"))
	var stages []Stage
	opts := Options{Progress: func(s Stage) { stages = append(stages, s) }}
	if err := start(context.Background(), e, provCfg("omlx", fp.serve(t)), Target{ProviderID: "omlx", ModelName: "B", ModelID: "omlx/B"}, opts); err != nil {
		t.Fatalf("start = %v, want nil once the load finishes", err)
	}
	if !reflect.DeepEqual(fp.loads, []string{"B", "B", "B"}) || !fp.isLoaded("B") {
		t.Errorf("loads = %v, B loaded = %v; want the load polled to completion", fp.loads, fp.isLoaded("B"))
	}
	if !fp.isLoaded("A") || len(*stopped) != 0 {
		t.Errorf("A loaded = %v, occupant hook = %v; want the sibling untouched", fp.isLoaded("A"), *stopped)
	}
	if !reflect.DeepEqual(stages, []Stage{StageWarming}) {
		t.Errorf("stages = %v, want [warming]: the caller's progress line must show the wait", stages)
	}
}

// TestStartOnALoadingTargetAsksNothing verifies a start that joins a load in
// progress is not refused for the models that load may evict. The start that
// began the load already put that question; asking again cannot change what
// omlx does, and a scripted second `wt start` would fail with "would stop A"
// for a load that is already under way. What the load did evict still loses
// its route when the load ends.
func TestStartOnALoadingTargetAsksNothing(t *testing.T) {
	captureRoutes(t)
	sizes := map[string]int64{"A": 60, "B": 60}
	fp := &fakePool{loaded: map[string]bool{"A": true, "B": false}, sizes: sizes, ceiling: 100, evictOnLoad: []string{"A"}}
	snap := loadingSnap(poolSnap(100, sizes, "A"), "B")
	target := Target{ProviderID: "omlx", ModelName: "B", ModelID: "omlx/B"}
	if victims, known := Evictions(target, snap); !known || len(victims) != 0 {
		t.Errorf("Evictions = %v known=%v, want none and known: the plan `wt start --plan` prints must agree with the start", victims, known)
	}
	e, stopped := poolEnv(t, snap)
	if err := start(context.Background(), e, provCfg("omlx", fp.serve(t)), target, Options{}); err != nil {
		t.Fatalf("start = %v, want nil without AllowReplace", err)
	}
	if !reflect.DeepEqual(fp.loads, []string{"B"}) || !reflect.DeepEqual(*stopped, []string{"omlx/A"}) {
		t.Errorf("loads = %v, occupant hook = %v; want [B] and the evicted [omlx/A]", fp.loads, *stopped)
	}
}

// TestALoadingSiblingStillOccupiesThePool verifies the other half of #259's
// rule: a model mid-load that is NOT the target is an occupant like any loaded
// one. Treating it as absent would let a start on a pool that is busy loading
// one model evict it unasked.
func TestALoadingSiblingStillOccupiesThePool(t *testing.T) {
	sizes := map[string]int64{"A": 60, "B": 60}
	snap := loadingSnap(poolSnap(100, sizes), "A")
	snap.OmlxPool.InUse = 60
	victims, known := Evictions(Target{ProviderID: "omlx", ModelName: "B"}, snap)
	if !known || len(victims) != 1 || victims[0].ModelID != "omlx/A" {
		t.Errorf("Evictions = %v known=%v, want the loading omlx/A named", victims, known)
	}
}

// TestStartOnALoadingTargetReportsHowTheLoadEnded verifies a start that joins
// a load in progress ends the way that load does. Before #259 it returned nil
// at once whatever happened next; now that it waits, each way the wait can end
// must reach the caller as itself — `wt start` prints "is running" on a nil
// return and nothing else. The sibling model is never touched.
func TestStartOnALoadingTargetReportsHowTheLoadEnded(t *testing.T) {
	sizes := map[string]int64{"A": 20, "B": 20}
	target := Target{ProviderID: "omlx", ModelName: "B", ModelID: "omlx/B"}
	busy := func(n int) []int {
		codes := make([]int, n)
		for i := range codes {
			codes[i] = http.StatusConflict
		}
		return codes
	}
	join := func(t *testing.T, ctx context.Context, fp *fakePool, tune func(*env)) error {
		t.Helper()
		e, stopped := poolEnv(t, loadingSnap(poolSnap(100, sizes, "A"), "B"))
		if tune != nil {
			tune(e)
		}
		err := start(ctx, e, provCfg("omlx", fp.serve(t)), target, Options{})
		if !fp.isLoaded("A") || len(*stopped) != 0 {
			t.Errorf("A loaded = %v, occupant hook = %v; want the sibling untouched", fp.isLoaded("A"), *stopped)
		}
		return err
	}

	t.Run("omlx runs out of room", func(t *testing.T) {
		fp := &fakePool{loaded: map[string]bool{"A": true, "B": false}, sizes: sizes, ceiling: 100, loadCodes: []int{http.StatusConflict, http.StatusInsufficientStorage}}
		var noRoom *NoRoomError
		if err := join(t, context.Background(), fp, nil); !errors.As(err, &noRoom) {
			t.Errorf("start = %v, want *NoRoomError", err)
		}
	})
	t.Run("the load never finishes", func(t *testing.T) {
		fp := &fakePool{loaded: map[string]bool{"A": true, "B": false}, sizes: sizes, ceiling: 100, loadCodes: busy(10000)}
		err := join(t, context.Background(), fp, nil)
		// Only the prefix: what follows is the last attempt's outcome, which is
		// omlx's 409 or the request the deadline cut short.
		if err == nil || !strings.HasPrefix(err.Error(), "timed out loading B into omlx: ") {
			t.Errorf("start = %v, want the load's timeout", err)
		}
	})
	t.Run("ctrl+c during the wait", func(t *testing.T) {
		fp := &fakePool{loaded: map[string]bool{"A": true, "B": false}, sizes: sizes, ceiling: 100, loadCodes: busy(10000)}
		ctx, cancel := context.WithCancel(context.Background())
		time.AfterFunc(30*time.Millisecond, cancel)
		// A budget far beyond the test, so only the cancel can end the wait.
		err := join(t, ctx, fp, func(e *env) { e.warmupTimeout = time.Minute })
		if !errors.Is(err, context.Canceled) {
			t.Errorf("start = %v, want context.Canceled", err)
		}
	})
}
```

- [ ] **Step 2: Run them and confirm they fail**

Run: `go test ./internal/lifecycle -run 'TestStartOnALoadingTarget|TestALoadingSiblingStillOccupiesThePool' -v`
Expected: FAIL. `TestStartOnALoadingTargetWaitsForTheLoad` reports `loads = [], B loaded = false; want the load polled to completion` and `stages = [], want [warming]: …`; `TestStartOnALoadingTargetAsksNothing` reports `Evictions = [...] known=true, want none and known` and `loads = [], occupant hook = []; want [B] and the evicted [omlx/A]`; `TestStartOnALoadingTargetReportsHowTheLoadEnded` reports `start = <nil>` in all three subtests. `TestALoadingSiblingStillOccupiesThePool` passes already: it guards behaviour this task must not change.

- [ ] **Step 3: Split "running" from "loading" in the engine**

In `lifecycle.go`, replace the whole of `isRunning` (lines 164-175, from its comment through its closing brace) with:

```go
// isRunning reports whether live Inventory already shows the target serving:
// running, and not still loading. A target omlx is mid-load on is not serving
// yet (#259); isLoading answers for it.
func isRunning(snap localmodels.Snapshot, family string, t Target) bool {
	return targetIs(snap, family, t, func(en localmodels.Entry) bool { return en.Running && !en.Loading })
}

// isLoading reports whether live Inventory shows the target mid-load. A start
// on such a target is not a no-op: it joins the load and returns when the
// model is loaded.
func isLoading(snap localmodels.Snapshot, family string, t Target) bool {
	return targetIs(snap, family, t, func(en localmodels.Entry) bool { return en.Running && en.Loading })
}

// targetIs reports whether a trusted snapshot has an entry for the target that
// satisfies is.
func targetIs(snap localmodels.Snapshot, family string, t Target, is func(localmodels.Entry) bool) bool {
	if !ProbeTrusted(snap, family) {
		return false
	}
	for _, en := range snap.Entries {
		if is(en) && localmodels.Family(en.ProviderID) == family && SameModel(family, en.ModelName, t.ModelName) {
			return true
		}
	}
	return false
}
```

`start` needs no other edit. Its `if isRunning(snap, family, t) { return nil }` (line 270) is now false for a loading target, so it goes on to `omlxBackend.start`, which finds the server answering, reports `StageWarming` and calls `omlxLoad`; that waits until the model is loaded, on a request omlx holds open or on its 409 answers. `reconcilePool` then runs as for any pool start.

- [ ] **Step 4: A loading target displaces nobody new**

In `evictions.go`, in `func evictions`, replace:

```go
	if !ProbeTrusted(snap, family) {
		return nil, false
	}
	others := runningOthers(snap, family, t)
```

with:

```go
	if !ProbeTrusted(snap, family) {
		return nil, false
	}
	// A target that is already loading was admitted by the start that began
	// the load: whatever omlx evicts for it is decided, and a second start
	// only waits. Planning it again would count the target's size on top of a
	// pool that may already hold it, and ask about models this start cannot
	// displace (#259). What the load did unload is found afterwards
	// (reconcilePool).
	if isLoading(snap, family, t) {
		return nil, true
	}
	others := runningOthers(snap, family, t)
```

- [ ] **Step 5: Bring the two doc comments up to date**

In `evictions.go`, in the comment on `Evictions`, replace the last line:

```go
// server refused the connection is the exception: that is known, and empty.
```

with:

```go
// server refused the connection is the exception: that is known, and empty.
// So is a target that is itself mid-load: it displaces nobody new.
```

In `lifecycle.go`, in the comment on `Start`, replace:

```go
// Start starts t. It is a no-op when live Inventory shows t already running,
// returns *OccupiedError (touching nothing) when it would replace a running
```

with:

```go
// Start starts t. It is a no-op when live Inventory shows t already running.
// A t that omlx is still loading is not running yet: Start joins that load
// and returns once the model is loaded, asking nothing (see evictions). It
// returns *OccupiedError (touching nothing) when it would replace a running
```

- [ ] **Step 6: Run the tests and confirm they pass**

Run: `go test ./internal/lifecycle -run 'TestStartOnALoadingTarget|TestALoadingSiblingStillOccupiesThePool' -v`
Expected: PASS.

Run: `go test ./...`
Expected: every line starts with `ok`. The catalog still makes a loading row a launch row, so no command reaches the new path yet.

- [ ] **Step 7: Commit**

```bash
test -z "$(gofmt -l .)" && go vet ./...
git add internal/lifecycle
git commit -m "fix(wt): a start on a loading omlx model waits for the load (#259)"
```

---

### Task 4: A loading row is a start row

**Files:**
- Modify: `wt/internal/catalog/catalog.go:29-47` (`ActionLaunch`'s comment, `Row`), `:99-117` and `:132-138` (`Build`), `:214-225` (`Action`)
- Modify: `wt/cmd/wt/model_cmds.go:234-235` (`startCmd`'s help)
- Test: `wt/internal/catalog/catalog_test.go` (append), `wt/internal/tui/start_flow_test.go` (append), `wt/internal/survey/stop_test.go` (append)
- Create: `wt/cmd/wt/loading_test.go`

**Interfaces:**
- Consumes:
  - `localmodels.Entry.Loading bool` (Task 2); `lifecycle.Evictions` returning `(nil, true)` for a loading target (Task 3)
  - existing: `func runStart(out io.Writer, cfg *config.Config, theme themes.Theme, id string, replace bool) error` (`model_cmds.go:390`), `func runStartJSON(out io.Writer, cfg *config.Config, id string, plan, replace bool) error` (`start_json.go:50`), `func resolveModel(agent string, cfg *config.Config, tags, family, pinned string) (config.Model, []config.Model, error)` (`resolve.go:56`), `func desiredLocalIDs(cfg *config.Config, snap localmodels.Snapshot) []string` (`litellm.go:324`)
  - test helpers: `stubProbeInventory`, `stubStartDriver(t, err) *startRequest`, `stubEnsureRoute(t)` (`cmd/wt/testmain_test.go`), `stubLifecycleStart(t, outcomes []error) *[]bool` (`cmd/wt/start_test.go:31`), `modelCmdConfig()`; in `internal/tui`: `stubInventory`, `stubStartModel`, `flowEnter`, `startCfg`, `indexOfID`, `itemIDs`, `updateMsg`, `waitStartCalls`; in `internal/survey`: `stopHarness`, `runningEntry`, `stopCandidates(cfg, deps)`
- Produces:
  - `catalog.Row.Loading bool`
  - `func (r Row) Ready() bool`: `r.Running && !r.Loading`. Task 5's `sortRows` uses it.
  - `Row.Action()` returns `ActionStart` for a local row that is running and loading (when its provider is startable)

- [ ] **Step 1: Write the failing catalog test**

Append to `wt/internal/catalog/catalog_test.go`:

```go
// TestALoadingRowIsAStartRow pins #259 for every model list wt builds. A model
// omlx is still loading keeps Running — it occupies the pool and stays listed
// — but selecting it must start (join the load and wait), not launch: a launch
// hands an agent a model that cannot answer yet. The flag has to survive all
// three ways Build makes a local row: a registry model, a discovered entry,
// and a discovered model handed back in through Models.
func TestALoadingRowIsAStartRow(t *testing.T) {
	inv := &localmodels.Snapshot{Entries: []localmodels.Entry{
		{ProviderID: "omlx", ModelID: "omlx/a", Artifact: "a", Registered: true, Running: true, Loading: true},
		{ProviderID: "omlx", ModelID: "omlx/disc", Artifact: "disc", Running: true, Loading: true},
		{ProviderID: "omlx", ModelID: "omlx/ready", Artifact: "ready", Running: true},
	}}
	registry := []config.Model{{ID: "omlx/a", ProviderID: "omlx", ModelName: "a"}}
	rows := Build(Input{Config: catalogTestCfg(), Agent: "claude", Models: registry, Inventory: inv})
	// Second pass: the rows' own models fed back in, as `wt start`'s picker does.
	var again []config.Model
	for _, r := range rows {
		again = append(again, r.Model)
	}
	for name, got := range map[string][]Row{"first build": rows, "rows fed back": Build(Input{Config: catalogTestCfg(), Models: again, Inventory: inv})} {
		if ids := strings.Join(rowIDs(got), ","); ids != "omlx/a,omlx/disc,omlx/ready" {
			t.Fatalf("%s: rows = %s", name, ids)
		}
		for _, r := range got[:2] {
			if !r.Running || !r.Loading || r.Ready() || r.Action() != ActionStart {
				t.Errorf("%s: %s Running=%v Loading=%v Ready=%v action=%v, want a running, loading start row", name, r.Model.ID, r.Running, r.Loading, r.Ready(), r.Action())
			}
		}
		if r := got[2]; r.Loading || !r.Ready() || r.Action() != ActionLaunch {
			t.Errorf("%s: %s Loading=%v Ready=%v action=%v, want a loaded launch row", name, r.Model.ID, r.Loading, r.Ready(), r.Action())
		}
	}
}
```

- [ ] **Step 2: Write the failing command tests**

Create `wt/cmd/wt/loading_test.go`:

```go
package main

import (
	"bytes"
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/localmodels"
	"github.com/ohanaverse/local-ai-setup/wt/internal/themes"
)

// loadingFixture stubs the inventory with one omlx model, omlx/c, that omlx is
// still loading: Running (it occupies the pool) and Loading. The pool reading
// is one in which a fresh start of it would need room (ceiling 100, 60 in use,
// 60 more wanted), so a test can tell "planned as a new start" from "joins the
// load".
func loadingFixture(t *testing.T) *config.Config {
	t.Helper()
	stubProbeInventory(t, localmodels.Snapshot{
		Providers: map[string]localmodels.Status{"omlx": localmodels.StatusOK},
		Entries: []localmodels.Entry{
			{ProviderID: "omlx", ModelID: "omlx/c", ModelName: "c", Artifact: "c", Registered: true, Running: true, Loading: true, ArtifactKnown: true},
			{ProviderID: "omlx", ModelID: "omlx/other", ModelName: "other", Artifact: "other", Running: true, ArtifactKnown: true},
		},
		OmlxPool: &localmodels.Pool{Ceiling: 100, InUse: 60, SizesKnown: true, Models: []localmodels.PoolModel{
			{ID: "c", Loading: true, Size: 60}, {ID: "other", Loaded: true, Size: 60},
		}},
	})
	return modelCmdConfig()
}

// TestStartOnALoadingModelWaitsInsteadOfSayingAlreadyRunning pins #259 for
// `wt start <id>`: on a model omlx is still loading it printed "already
// running" and returned at once, so a script that started a model and then
// used it raced the load. It now goes through the start driver, which returns
// when the model is loaded, and reports it as running.
func TestStartOnALoadingModelWaitsInsteadOfSayingAlreadyRunning(t *testing.T) {
	cfg := loadingFixture(t)
	req := stubStartDriver(t, nil)
	var out bytes.Buffer
	if err := runStart(&out, cfg, themes.Theme{}, "omlx/c", false); err != nil {
		t.Fatal(err)
	}
	if !req.called || req.row.Model.ID != "omlx/c" {
		t.Fatalf("start driver called = %v for %q, want it called for omlx/c", req.called, req.row.Model.ID)
	}
	if got := out.String(); strings.Contains(got, "already running") || !strings.Contains(got, "wt: omlx/c is running") {
		t.Errorf("out = %q, want the is-running line once the load is done, not already-running", got)
	}
}

// TestStartJSONOnALoadingModelPlansNothingAndWaits pins #259 for the form
// modelman drives. The dry run used to answer "running", so `modelman start`
// reported a model as started while it was still loading. It now answers
// "fits" with nothing to unload — the load is already admitted, so the pool's
// other model is not named — and the real start runs the engine, which waits,
// and answers "started".
func TestStartJSONOnALoadingModelPlansNothingAndWaits(t *testing.T) {
	cfg := loadingFixture(t)
	started := stubLifecycleStart(t, nil)
	stubEnsureRoute(t)

	var plan bytes.Buffer
	if err := runStartJSON(&plan, cfg, "omlx/c", true, false); err != nil {
		t.Fatal(err)
	}
	var p startPlanJSON
	if err := json.Unmarshal(plan.Bytes(), &p); err != nil {
		t.Fatalf("plan %q is not JSON: %v", plan.String(), err)
	}
	if p.Status != "fits" || len(p.WouldUnload) != 0 || len(*started) != 0 {
		t.Errorf("plan = %+v, engine calls = %d; want fits, nothing to unload, nothing started", p, len(*started))
	}

	var out bytes.Buffer
	if err := runStartJSON(&out, cfg, "omlx/c", false, false); err != nil {
		t.Fatalf("start without --replace = %v, want it to join the load unasked", err)
	}
	var r startResultJSON
	if err := json.Unmarshal(out.Bytes(), &r); err != nil {
		t.Fatalf("result %q is not JSON: %v", out.String(), err)
	}
	if r.Status != "started" || len(*started) != 1 {
		t.Errorf("result = %+v, engine calls = %d; want started after one engine call", r, len(*started))
	}
}

// TestALoadingModelIsNotLaunchableButAPinStartsIt pins #259 for the non-TUI
// launch. Without -M, wt picks only among models that can answer now, so a
// model mid-load must not be chosen (the agent's first request would hang on
// the load). With -M the pin starts it, which waits for the load, exactly as
// a pin on an idle model does.
func TestALoadingModelIsNotLaunchableButAPinStartsIt(t *testing.T) {
	cfg := loadingFixture(t)
	cfg.Agents = []config.Agent{{Name: "pi", SupportedProviders: []string{"omlx"}}}
	// A base url, so the pin's route guard resolves a launch route; without
	// one the row is refused before the start (pickerBlockedReason).
	cfg.Providers[1].Auth.BaseURL = "http://localhost:8000"
	req := stubStartDriver(t, nil)

	_, launchable, _ := resolveModel("pi", cfg, "", "", "")
	ids := make([]string, len(launchable))
	for i, m := range launchable {
		ids[i] = m.ID
	}
	if !slices.Equal(ids, []string{"omlx/other"}) {
		t.Errorf("launchable = %v, want only the loaded omlx/other", ids)
	}
	if req.called {
		t.Fatal("resolving without a pin must never start a model")
	}

	m, _, err := resolveModel("pi", cfg, "", "", "omlx/c")
	if err != nil || m.ID != "omlx/c" || !req.called {
		t.Errorf("pin: model = %q err = %v started = %v, want omlx/c started and returned", m.ID, err, req.called)
	}
}

// TestALoadingModelKeepsItsRoute verifies the half of #259 that must not
// change: a model mid-load is still in the set `wt litellm sync` routes.
// Dropping it would remove the route a start wrote moments ago and restart the
// proxy under every other session, only to add the route back when the load
// ends.
func TestALoadingModelKeepsItsRoute(t *testing.T) {
	cfg := loadingFixture(t)
	if got := desiredLocalIDs(cfg, probeInventory(cfg)); !slices.Equal(got, []string{"omlx/c", "omlx/other"}) {
		t.Errorf("desiredLocalIDs = %v, want the loading omlx/c beside omlx/other", got)
	}
}
```

- [ ] **Step 3: Write the failing picker test and the stop guard**

Append to `wt/internal/tui/start_flow_test.go`:

```go
// TestEnterOnALoadingRowJoinsTheLoad drives #259 through the real picker: the
// inventory reports the model mid-load, and Enter on its row must run the
// start flow (whose engine waits for the load) instead of launching the agent
// on a model that cannot answer yet. The row is not marked by hand: it is a
// start row because the catalog says a loading row is.
func TestEnterOnALoadingRowJoinsTheLoad(t *testing.T) {
	stubInventory(t, localmodels.Snapshot{Entries: []localmodels.Entry{
		{ProviderID: "omlx", Artifact: "qwen3.8", ModelID: "omlx/qwen3.8", Registered: true, Running: true, Loading: true},
	}})
	calls := stubStartModel(t, func(int, context.Context, lifecycle.Target, lifecycle.Options) error { return nil })
	m := flowEnter(t, model{cfg: startCfg("omlx", "omlx/qwen3.8", "qwen3.8"), agent: "claude", selectedPath: t.TempDir(), width: 80, height: 24}, "claude")
	idx := indexOfID(m, "omlx/qwen3.8")
	if idx < 0 {
		t.Fatalf("no row for the loading model in %v", itemIDs(m))
	}
	m.models.Select(idx)
	got, _ := updateMsg(m, tea.KeyMsg{Type: tea.KeyEnter})
	if got.phase != phaseStarting {
		t.Fatalf("phase = %v, want phaseStarting: Enter on a loading row must start, not launch", got.phase)
	}
	waitStartCalls(t, calls, 1)
	if id := calls.at(0).target.ModelID; id != "omlx/qwen3.8" {
		t.Errorf("started %q, want omlx/qwen3.8", id)
	}
}
```

Append to `wt/internal/survey/stop_test.go`:

```go
// TestStopCandidatesIncludeAModelMidLoad verifies the loading flag does not
// hide a model from `wt stop`: it stays a stop candidate, in `wt stop <id>` and
// in the stop picker. A user who sees the model occupying the pool must be able
// to name it there. (omlx 0.7.0 refuses to unload a model mid-load, so the stop
// itself reports `omlx still has <id> loaded`; `wt stop omlx` is what calls a
// load off.)
func TestStopCandidatesIncludeAModelMidLoad(t *testing.T) {
	loading := runningEntry("omlx", "omlx/x", "x")
	loading.Loading = true
	h := &stopHarness{snap: localmodels.Snapshot{Entries: []localmodels.Entry{loading}}}
	cands := stopCandidates(&config.Config{}, h.deps())
	if len(cands) != 1 || cands[0].Entry.ModelID != "omlx/x" {
		t.Errorf("candidates = %+v, want the loading omlx/x", cands)
	}
}
```

- [ ] **Step 4: Run them and confirm they fail**

Run: `go test ./internal/catalog ./cmd/wt ./internal/tui ./internal/survey -run 'Loading|MidLoad'`
Expected: `internal/catalog` fails to build with `r.Loading undefined (type Row has no field or method Loading)` and `r.Ready undefined`. `cmd/wt` fails three tests:

```
TestStartOnALoadingModelWaitsInsteadOfSayingAlreadyRunning: start driver called = false for "", want it called for omlx/c
TestStartJSONOnALoadingModelPlansNothingAndWaits: plan = {ID:omlx/c Status:running WouldUnload:[]}, engine calls = 0; want fits, nothing to unload, nothing started
TestStartJSONOnALoadingModelPlansNothingAndWaits: result = {… Status:already_running …}, engine calls = 0; want started after one engine call
TestALoadingModelIsNotLaunchableButAPinStartsIt: launchable = [omlx/c omlx/other], want only the loaded omlx/other
TestALoadingModelIsNotLaunchableButAPinStartsIt: pin: model = "omlx/c" err = <nil> started = false, want omlx/c started and returned
```

`internal/tui` fails `TestEnterOnALoadingRowJoinsTheLoad` with `phase = …, want phaseStarting`. `TestALoadingModelKeepsItsRoute` (`cmd/wt`) and `TestStopCandidatesIncludeAModelMidLoad` (`internal/survey`) pass already: they guard behaviour this task must not change.

- [ ] **Step 5: Add `Loading` and `Ready` to the row**

In `catalog.go`, replace the `ActionLaunch` line:

```go
	ActionLaunch Action = iota // cloud row, or a local model already running
```

with:

```go
	ActionLaunch Action = iota // cloud row, or a local model that is running and loaded
```

Replace the whole `Row` type (from `// Row is one model-selector row before rendering.` through its closing brace) with the following. The field alignment is what `gofmt` produces once a comment splits the block:

```go
// Row is one model-selector row before rendering.
type Row struct {
	Model    config.Model
	Location config.Location
	Status   Status
	Running  bool
	// Loading: the model is mid-load on omlx (localmodels.Entry.Loading,
	// #259). Running is true too; Ready is what says it can take a request.
	Loading    bool
	Discovered bool
	// Unmapped marks a cloud row whose provider has no LiteLLM mapping: it
	// is in the catalog (every configured model is, #179) but sync never
	// routes it, so a launch through the proxy would fail with "Invalid
	// model name". RefusedByRoute refuses it there; direct, it launches.
	Unmapped bool
}
```

Replace the whole of `Action` (its comment and body, lines 214-225) with:

```go
// Ready reports whether the row's model can take a request now: running, and
// not still loading. A cloud row is never Running, so it is never Ready; its
// launch does not depend on this.
func (r Row) Ready() bool { return r.Running && !r.Loading }

// Action reports what selecting r does. A local row that is not Ready is
// startable when its provider has a lifecycle backend (a model missing from
// disk has no row at all). That includes a model omlx is still loading (#259):
// starting it joins the load in progress and returns once the model is
// loaded, where launching it would hand an agent a model that cannot answer
// yet.
func (r Row) Action() Action {
	if r.Location != config.LocationLocal || r.Ready() {
		return ActionLaunch
	}
	if !startable(r.Model.ProviderID) {
		return ActionBlock
	}
	return ActionStart
}
```

- [ ] **Step 6: Carry the flag through `Build`**

`Build` makes a local row in three places. In `catalog.go`, replace:

```go
			r.Status, r.Running, r.Discovered = StatusNew, de.Running, true
```

with:

```go
			r.Status, r.Running, r.Loading, r.Discovered = StatusNew, de.Running, de.Loading, true
```

Replace:

```go
			r.Running = e.Running
			if !e.Running && !e.ArtifactKnown {
```

with:

```go
			r.Running, r.Loading = e.Running, e.Loading
			if !e.Running && !e.ArtifactKnown {
```

Replace:

```go
				Location: config.LocationLocal, Status: StatusNew, Running: e.Running, Discovered: true,
```

with:

```go
				Location: config.LocationLocal, Status: StatusNew, Running: e.Running, Loading: e.Loading, Discovered: true,
```

`listed` and `MissingReason` read `e.Running` and stay as they are: a loading model has a row.

- [ ] **Step 7: Say so in `wt start --help`**

In `cmd/wt/model_cmds.go`, in `startCmd`'s `Long`, replace:

```go
			"written if it is missing.\n\n" +
```

with:

```go
			"written if it is missing. A model omlx is still loading is waited for.\n\n" +
```

`runStart` itself needs no edit: its `switch row.Action()` (line 422) now takes the `ActionStart` arm for a loading row, which calls `startModel` and prints `wt: <id> is running`.

- [ ] **Step 8: Run the tests and confirm they pass**

Run: `go test ./internal/catalog ./cmd/wt ./internal/tui ./internal/survey -run 'Loading|MidLoad' -v`
Expected: PASS.

Run: `go test ./...`
Expected: every line starts with `ok`.

- [ ] **Step 9: Commit**

```bash
test -z "$(gofmt -l .)" && go vet ./...
git add internal/catalog cmd/wt internal/tui/start_flow_test.go internal/survey/stop_test.go
git commit -m "fix(wt): a loading omlx model is a start row, not a launch row (#259)"
```

---

### Task 5: The picker shows `load`, and the docs say what loading means

**Files:**
- Modify: `wt/internal/tui/modelrows.go:102-117` (`sortRows`)
- Modify: `wt/internal/tui/modeltable.go:148-153` (`flag`, removed), `:259` (the RUNNING cell)
- Test: `wt/internal/tui/modelrows_test.go` (append), `wt/internal/tui/modeltable_test.go` (append, one import)
- Modify: `wt/docs/internals/local-models.md`, `wt/docs/wt-start-stop.md`, `wt/docs/wt-smoke.md`, `wt/CLAUDE.md`, `wt/CHANGELOG.md`, `docs/guides/06-wt-agents-and-models.md`

**Interfaces:**
- Consumes: `catalog.Row.Loading`, `func (r catalog.Row) Ready() bool` (Task 4); existing `tableRow` (embeds `catalog.Row`), `func renderTable(rows []tableRow, cfg *config.Config, agent string, refs map[string]int, lastID string) modelTable`, `func sortRows(rows []tableRow)`, test helpers `tableTestRows()`, `rowIDs`, `f64`
- Produces: `func runningText(r catalog.Row) string` returning `run`, `load` or `-`. The name is not `runningCell`: a test helper of that name already exists in `table_refresh_test.go:15`.

- [ ] **Step 1: Write the failing tests**

In `wt/internal/tui/modeltable_test.go`, add `"slices"` to the import block, between `"path/filepath"` and `"strings"`. Then append:

```go
// TestRenderTableShowsALoadingModelAsLoad pins #259 in the picker. A model
// omlx is still loading used to render as "run" and launch on Enter, handing
// the agent a model that could not answer. Its RUNNING cell now reads "load"
// and Enter starts it, which joins the load and waits. "load" fits the
// column's fixed width, so the header and the other rows do not move.
func TestRenderTableShowsALoadingModelAsLoad(t *testing.T) {
	rows := tableTestRows()
	// omlx/Qwen3.8-27B-4bit: Running and Loading. The shared rows leave its
	// provider empty, which a launch row never needs and a start row does.
	rows[1].Loading, rows[1].Model.ProviderID = true, "omlx"
	tbl := renderTable(rows, nil, "", nil, "")
	it := tbl.items[1]
	fields := strings.Fields(it.line)
	if !slices.Contains(fields, "load") || slices.Contains(fields, "run") {
		t.Errorf("loading line = %q, want a RUNNING cell of load", it.line)
	}
	if !it.start || it.blocked != "" {
		t.Errorf("loading row: start = %v blocked = %q, want a start row", it.start, it.blocked)
	}
	if plain := renderTable(tableTestRows(), nil, "", nil, ""); plain.header != tbl.header || len(plain.items[0].line) != len(tbl.items[0].line) {
		t.Errorf("a loading row changed the layout:\n%q\n%q", plain.header, tbl.header)
	}
}
```

Append to `wt/internal/tui/modelrows_test.go`:

```go
// TestSortRowsPutsALoadingModelWithTheStartRows pins where a model that omlx
// is still loading sorts (#259). The sort order is also the default selection,
// and group 1 is what a bare Enter launches at once: a loading model left
// there sorts first on its $0 cost and becomes the default pick while it
// cannot answer. It belongs with the other rows Enter starts.
func TestSortRowsPutsALoadingModelWithTheStartRows(t *testing.T) {
	local := func(id string, running, loading bool) tableRow {
		return tableRow{Row: catalog.Row{Location: config.LocationLocal, Running: running, Loading: loading, Model: config.Model{ID: id}}}
	}
	rows := []tableRow{
		local("m-loading", true, true),
		local("z-off", false, false),
		{Row: catalog.Row{Location: config.LocationCloud, Model: config.Model{ID: "cloud", Cost: config.ModelCost{OutputPricePerMillion: f64(1)}}}},
		local("run", true, false),
		local("a-off", false, false),
	}
	sortRows(rows)
	if got, want := strings.Join(rowIDs(rows), ","), "run,cloud,a-off,m-loading,z-off"; got != want {
		t.Errorf("order = %s\nwant    %s", got, want)
	}
}
```

- [ ] **Step 2: Run them and confirm they fail**

Run: `go test ./internal/tui -run 'TestRenderTableShowsALoadingModelAsLoad|TestSortRowsPutsALoadingModelWithTheStartRows' -v`
Expected: FAIL with

```
order = m-loading,run,cloud,a-off,z-off
want    run,cloud,a-off,m-loading,z-off
```

and `loading line = "qwen    omlx/Qwen3.8-27B-4bit  local  ok       run  …", want a RUNNING cell of load`.

- [ ] **Step 3: Sort a loading row with the start rows**

In `modelrows.go`, in `sortRows`, replace:

```go
	group1 := func(r tableRow) bool { return r.Location != config.LocationLocal || r.Running }
```

with:

```go
	// Ready, not Running: a model omlx is still loading is a start row (#259)
	// and sorts with them, so it is never the default pick while it cannot
	// answer.
	group1 := func(r tableRow) bool { return r.Location != config.LocationLocal || r.Ready() }
```

In the comment above `sortRows`, replace:

```go
// each half then falls through to the same group rules — group 1 (cloud +
// running local) by cost ascending then 7-day usage ascending then id; group 2
// (non-running local) alphabetical by id — so a native row that resolves local
// and is not running still sorts after the native group-1 rows, by id, not
// "in group-1 order".
```

with:

```go
// each half then falls through to the same group rules — group 1 (cloud +
// running local) by cost ascending then 7-day usage ascending then id; group 2
// (local that is not running, or still loading) alphabetical by id — so a
// native row that resolves local and is not running still sorts after the
// native group-1 rows, by id, not "in group-1 order".
```

- [ ] **Step 4: Render the RUNNING cell**

In `modeltable.go`, delete `flag`, which has no other caller:

```go
func flag(b bool, yes string) string {
	if b {
		return yes
	}
	return "-"
}
```

and put this in its place:

```go
// runningText is a row's RUNNING cell: "run" for a model that is serving,
// "load" for one omlx is still loading (#259; Enter starts it, which waits for
// the load), "-" otherwise. Both words fit the column's fixed width of 7.
func runningText(r catalog.Row) string {
	switch {
	case r.Running && r.Loading:
		return "load"
	case r.Running:
		return "run"
	}
	return "-"
}
```

In `renderTable`, replace:

```go
			padRunes(flag(r.Running, "run"), 7), padRunes(cost[i], costW),
```

with:

```go
			padRunes(runningText(r.Row), 7), padRunes(cost[i], costW),
```

- [ ] **Step 5: Run the tests and confirm they pass**

Run: `go test ./internal/tui -run 'TestRenderTableShowsALoadingModelAsLoad|TestSortRowsPutsALoadingModelWithTheStartRows' -v`
Expected: PASS.

Run: `go build ./... && go test ./...`
Expected: every line starts with `ok`. `TestEveryListPhaseFitsTheTerminal` and `TestModelTableUnchangedWhenItFits` still pass: `load` fits the column's fixed width of 7.

- [ ] **Step 6: Commit the code**

```bash
test -z "$(gofmt -l .)" && go vet ./...
git add internal/tui
git commit -m "fix(wt): the picker shows a loading model as load and sorts it with the start rows (#259)"
```

- [ ] **Step 7: Update `wt/docs/internals/local-models.md`**

Replace the `launch` and `start` bullets (lines 10-11):

```markdown
- **launch** — a cloud row, or a local model the probe reports running.
- **start** — a non-running local row of ollama/omlx/omlx-6bit/mtplx (a pulled ollama model is a start, not a launch).
```

with:

```markdown
- **launch** — a cloud row, or a local model the probe reports running and not still loading (`catalog.Row.Ready`).
- **start** — a non-running local row of ollama/omlx/omlx-6bit/mtplx (a pulled ollama model is a start, not a launch), or an omlx model that is still loading (#259, see "Loading is not loaded" below).
```

After the paragraph that begins **omlx's `/v1/models` is not what is running (#201).** (line 18), insert a blank line and this paragraph:

```markdown
**Loading is not loaded (#259).** `localmodels.Entry.Loading`, copied to `catalog.Row.Loading`, is true when omlx's status reading lists the model `is_loading` and not `loaded` (`source.isLoading`, which asks `Pool.Find`, so the name resolves to the pool model a load of it would act on). `Running` is true for it too, and everything that reads `Running` alone is unchanged: the model occupies the pool (`runningOthers`, `poolVictims`), is listed (`catalog.listed`), keeps its route (`desiredLocalEntries`) and is still offered by `wt stop` (`survey.StopCandidates`). Offered is not the same as stopped: omlx 0.7.0 answers an unload of a model mid-load with `400 Model not loaded`, `omlxUnload` then finds it still in the pool, and the stop fails with `omlx still has <id> loaded` while the load goes on. `wt stop omlx` halts the service and the load with it. Three things read `Loading`. `catalog.Row.Ready()` is `Running && !Loading`, and `Row.Action()` launches only a `Ready` local row, so a loading row is a **start** row: `launchableModels` leaves it out, a `-M` pin starts it, and `sortRows` puts it with the start rows; its RUNNING cell reads `load`. `lifecycle.isRunning` is false for a loading target, so `start` goes on to `omlxLoad`, which returns when the model is loaded — omlx 0.7.0 holds the second load request open until then, and a 409 or 503 answer is polled — so a start on a loading model waits for the load. And `evictions` names no victims for a target that is itself loading: the start that began the load already asked, so the one that joins it asks nothing and `wt start <id> --plan --json` answers `fits`. Only the status reading can see a load. Under the fallback reading (`SizesKnown` false) a model mid-load reads as not running, as it did before, and is never `Loading`.
```

Replace the `Columns` and `Sort` bullets (lines 45-46):

```markdown
- Columns: FAMILY, MODEL, LOC, STATUS, RUNNING, COST, 1D, 7D, 30D, SURVEY. `wt smoke`'s picker has no agent context: SURVEY is empty and usage uses model-level `Counts`.
- Sort: native models first (#172); then cloud + running local by cost (output then input price; local/subscription-only = $0; no-data last), then 7-day usage; non-running local alphabetical. Native-first is a *partition*, not an exemption: native rows still fall through to the group rules among themselves.
```

with:

```markdown
- Columns: FAMILY, MODEL, LOC, STATUS, RUNNING, COST, 1D, 7D, 30D, SURVEY. RUNNING reads `run`, `load` (omlx is still loading the model, `runningText`) or `-`. `wt smoke`'s picker has no agent context: SURVEY is empty and usage uses model-level `Counts`.
- Sort: native models first (#172); then cloud + running local by cost (output then input price; local/subscription-only = $0; no-data last), then 7-day usage; local that is not running, or still loading, alphabetical. Native-first is a *partition*, not an exemption: native rows still fall through to the group rules among themselves.
```

In the bullet that begins ``- `wt start <id> --json` (`start_json.go`, `runStartJSON`) never prompts.`` (line 58), append this sentence to the end of the bullet:

```markdown
 A model that is still loading plans as `fits` with nothing to unload, and its start waits for the load and prints `started` (#259).
```

- [ ] **Step 8: Update `wt/docs/wt-start-stop.md`**

After the `Already running` bullet (lines 32-34):

```markdown
- Already running: the model is left running. Its LiteLLM route is written
  if it is missing (`wt: LiteLLM route for <id> updated` on stderr), then wt
  prints `wt: <id> is already running` and exits 0.
```

insert:

```markdown
- Still loading (omlx): the model cannot answer yet, so `wt start <id>` waits
  for the load that is already in progress, with the usual progress on
  stderr, then prints `wt: <id> is running`. It asks nothing, because it
  loads nothing new. In the pickers the row's RUNNING column reads `load`,
  and selecting it waits the same way. `wt` without `-M` never picks a model
  that is still loading. `wt stop <id>` on it fails with
  `omlx still has <id> loaded`, because omlx does not unload a model in the
  middle of a load; `wt stop omlx` stops the service and the load with it.
  wt sees a load only through omlx's status endpoint: on a server with an
  API key the registry does not name, a loading model reads as not running.
```

In the section headed "`--plan` and `--json`" (line 138), after the paragraph that ends ``status but `would_unload`.``, add a new paragraph:

```markdown
A model omlx is still loading reports `fits` with an empty `would_unload`.
Starting it waits for the load and prints `started`.
```

- [ ] **Step 9: Update `wt/CLAUDE.md` and `wt/CHANGELOG.md`**

In `wt/CLAUDE.md`, section "Local-model resolution", replace:

```markdown
A row's action is **launch** (cloud, or a local model the probe reports running), **start** (a non-running local row of ollama/omlx/omlx-6bit/mtplx), or **block**.
```

with:

```markdown
A row's action is **launch** (cloud, or a local model the probe reports running and loaded), **start** (a local row of ollama/omlx/omlx-6bit/mtplx that is not running, or that omlx is still loading), or **block**.
```

In `wt/CHANGELOG.md`, under `## Unreleased`, add as the first bullet of `### Fixed`:

```markdown
- An omlx model that is still loading is no longer treated as loaded (#259).
  `wt start` on it waits for the load instead of printing `already running`,
  and the pickers show `load` in the RUNNING column and start it on Enter
  instead of launching an agent on a model that cannot answer yet. It still
  counts as occupying the pool and keeps its route.
```

- [ ] **Step 10: Update the user guide and `wt/docs/wt-smoke.md`**

Both describe the rule this PR changes. In `docs/guides/06-wt-agents-and-models.md`, in the long paragraph that begins `The tag slot shows the **first** tag` (line 68), replace:

```markdown
non-running local models form a second group, sorted by id.
```

with:

```markdown
local models that are not running, or that omlx is still loading, form a second group, sorted by id.
```

and, in the same paragraph, replace:

```markdown
RUNNING is `run` while a model is serving.
```

with:

```markdown
RUNNING is `run` while a model is serving and `load` while omlx is still loading it (Enter waits for the load).
```

Neither sentence names a model or a count, so the guide stays independent of live model state.

In `wt/docs/wt-smoke.md`, replace:

```markdown
`supported_providers` and the row is a launch row (cloud, or a local model
the live probe reports running) or a start row (an idle local model wt can
start). Rows that cannot run
```

with:

```markdown
`supported_providers` and the row is a launch row (cloud, or a local model
the live probe reports running and loaded) or a start row (an idle local
model wt can start, or one omlx is still loading). Rows that cannot run
```

and replace:

```markdown
An idle pick is started first through the shared start driver, honouring
```

with:

```markdown
An idle pick, or one omlx is still loading, is started first through the
shared start driver, honouring
```

`wt smoke` needs no code edit: its `switch t.Row.Action()` (`cmd/wt/smoke.go:327`) takes the start arm for a loading row, as `runStart` does, and waits for the load.

- [ ] **Step 11: Check and commit the docs**

Run from the monorepo root: `make lint`
Expected: shell lint passes and the link check prints `ALL LINKS OK`.

```bash
git add wt/docs wt/CLAUDE.md wt/CHANGELOG.md docs/guides/06-wt-agents-and-models.md
git commit -m "docs(wt): loading is not loaded (#259)"
```

- [ ] **Step 12: Verify as CI does**

Run from `wt/`:

```bash
gofmt -l .
go build ./...
go vet ./...
go test ./...
make check
```

Expected: `gofmt -l .` prints nothing; every `go test` line starts with `ok`; `make check` passes.

Run from the monorepo root: `make test-all`
Expected: lint passes, the modelman suite passes (modelman is untouched and its `wt start --json` contract test reads an unchanged fixture), and the wt lines all start with `ok`.

Do not hand the branch off yet. Task 6 checks it against a real omlx first.

---

### Task 6: Check #259 against a real omlx, then hand PR B off (needs the owner)

The fake servers cover every path above. This task confirms what a fake cannot: how a real omlx answers a load, and an unload, of a model that is already loading, and that the picker reads well on a real screen. The fix rests on the first of those. If omlx answered a second load with a code `omlxLoad` does not handle (`omlxpool.go:115-126`), `wt start` on a loading model would change from printing "already running" to failing, and `modelman start` with it. So this check gates the handoff: PR B is not handed off until Step 3's second start has returned `wt: omlx/<model> is running`, or the owner has said to hand off without the live check.

It touches a live provider, so **do nothing here until the owner says to**, and never without the two redirects in Step 2.

**Files:** none, unless Step 4's observation contradicts a doc sentence Task 5 wrote.

**Interfaces:**
- Consumes: this branch, built locally, with Tasks 2 to 5 committed.
- Produces: the observations Step 6 reports, and PR B's handoff.

- [ ] **Step 1: Ask the owner**

Ask three things and wait for the answers:

1. Which omlx model to use. It must not be loaded now, and a large one loads slowly enough to observe (20 GB or more is comfortable).
2. Whether omlx may be started and that model loaded now.
3. Whether omlx was running before, so Step 5 can leave it as it was found.

If the owner says not to run the live check, skip to Step 6 and say so in the handoff.

- [ ] **Step 2: Build the branch binary, redirect its config, and write the screen driver**

`~/.local/bin/wt` is whatever was last installed, so build the branch. Run from `wt/`:

```bash
go build -o /tmp/wt-verify ./cmd/wt
```

Then, in each terminal used below, point wt at scratch copies. Both variables are mandatory: a redirected registry without `WT_LITELLM_CONFIG` is refused, and the real `config.yaml` must not be rewritten.

```bash
export XDG_CONFIG_HOME="$(mktemp -d)"
mkdir -p "$XDG_CONFIG_HOME/local-ai" "$XDG_CONFIG_HOME/agent-wt"
cp ~/.config/local-ai/registry.toml "$XDG_CONFIG_HOME/local-ai/"
cp ~/.config/agent-wt/config.toml "$XDG_CONFIG_HOME/agent-wt/"
cp ~/.config/litellm/config.yaml "$XDG_CONFIG_HOME/litellm-config.yaml"
export WT_LITELLM_CONFIG="$XDG_CONFIG_HOME/litellm-config.yaml"
export WT_LITELLM_RESTART_CMD=true
```

Use the same `XDG_CONFIG_HOME` value in the second terminal (copy it across; do not run `mktemp` twice).

Step 4 reads a full-screen picker without a person at the keyboard. Nothing in the repo does that, so write the driver. It runs a command in an 80x24 pty, answers the two terminal queries wt waits on before it draws, prints the screen it shows after three seconds with each line's width in front, and kills the command without choosing anything:

```bash
cat > /tmp/wt-screen.py <<'PY'
"""Run a full-screen wt command in an 80x24 pty and print the screen it shows
after three seconds. Usage: python3 wt-screen.py <command> [args...]"""
import fcntl, os, pty, re, select, signal, struct, sys, termios, time

pid, fd = pty.fork()
if pid == 0:
    # The child's stdin is the pty: size it before wt reads the size.
    fcntl.ioctl(0, termios.TIOCSWINSZ, struct.pack("HHHH", 24, 80, 0, 0))
    os.environ["TERM"] = "xterm-256color"
    os.execvp(sys.argv[1], sys.argv[1:])

out, answered, end = b"", False, time.time() + 3
while time.time() < end:
    if not select.select([fd], [], [], 0.1)[0]:
        continue
    try:
        chunk = os.read(fd, 65536)
    except OSError:
        break
    if not chunk:
        break
    out += chunk
    # wt asks the terminal for its background colour (OSC 11) and the cursor
    # position before it draws; without both answers it waits.
    if not answered and b"\x1b]11;?" in out and b"\x1b[6n" in out:
        os.write(fd, b"\x1b]11;rgb:0000/0000/0000\x1b\\\x1b[1;1R")
        answered = True
# The screen is captured: end wt without choosing anything.
os.kill(pid, signal.SIGKILL)
os.waitpid(pid, 0)

# Keep what follows the last full redraw and drop the escape sequences.
text = out.decode("utf-8", "replace")
text = text[text.rfind("\x1b[H") + 3:] if "\x1b[H" in text else text
text = re.sub(r"\x1b\][^\x07\x1b]*(\x07|\x1b\\)|\x1b\[[0-9;?]*[A-Za-z]|\x1b[=>]", "", text)
for line in text.replace("\r", "").split("\n"):
    print(f"{len(line):3d} |{line}")
PY
```

- [ ] **Step 3: A second start waits**

Terminal 1:

```bash
/tmp/wt-verify start omlx/<model>
```

While terminal 1 still prints `wt: starting omlx/<model> — warming the model`, in terminal 2:

```bash
/tmp/wt-verify start omlx/<model> --plan --json   # expect "status": "fits", "would_unload": []
/tmp/wt-verify start omlx/<model>                 # expect progress lines, then "wt: omlx/<model> is running"
```

Expected: terminal 2's start does not print `already running`, and returns at about the moment terminal 1 does. Run `/tmp/wt-verify start omlx/<model>` once more after both return.
Expected: `wt: omlx/<model> is already running`.

omlx 0.7.0 holds its pool lock for the whole load, so terminal 2's load request normally blocks and returns 200 when the model is loaded; a 409 is also waited for. If terminal 2's start returns before terminal 1's, or fails with `omlx could not load …: HTTP <code>`, record the code and the message, report it to the owner, and do not hand PR B off. Do not change the code in this task.

- [ ] **Step 4: The picker shows `load`, and what `wt stop` says mid-load**

Stop the model (`/tmp/wt-verify stop omlx/<model> --yes`) and start it again in terminal 1. While it loads, in terminal 2:

```bash
python3 /tmp/wt-screen.py /tmp/wt-verify start
```

Expected: the model's row reads `load` in the RUNNING column, and every printed line is at most 80 wide (the number in front of each line).

Still while it loads, in terminal 2:

```bash
/tmp/wt-verify stop omlx/<model> --yes
```

Record exactly what it prints. Expected with omlx 0.7.0: it fails with a message ending `omlx still has <id> loaded`, and terminal 1's start goes on to finish. If it does anything else (the stop succeeds, or the message differs), correct the two sentences Task 5 wrote about this (the "Still loading" bullet in `wt/docs/wt-start-stop.md` and the "Loading is not loaded" paragraph in `wt/docs/internals/local-models.md`) to say what was observed, run `make lint` from the monorepo root, and commit:

```bash
git add wt/docs
git commit -m "docs(wt): what wt stop does to a model omlx is still loading (#259)"
```

- [ ] **Step 5: Clean up**

Wait for terminal 1's start to return, then:

```bash
/tmp/wt-verify stop omlx/<model> --yes
rm -rf "$XDG_CONFIG_HOME" /tmp/wt-verify /tmp/wt-screen.py
unset XDG_CONFIG_HOME WT_LITELLM_CONFIG WT_LITELLM_RESTART_CMD
```

Run the `unset` line in both terminals. If omlx was not running before this task (Step 1), also run `wt stop omlx`. Then, with the real environment, confirm the real routes were never touched:

```bash
wt litellm sync --dry-run
```

Expected: no route to add or remove. If it lists a change, tell the owner what it lists; do not run the real sync without their OK.

- [ ] **Step 6: Hand off**

Stop here. Tell the owner the branch is ready, what `make test-all` printed (Task 5 Step 12), and what Steps 3 and 4 showed: the plan and the second start's output, the `load` row, and what `wt stop` printed mid-load. If the live check was not run, say that the wait on a real omlx is covered by fake-server tests and a reading of omlx 0.7.0's source only. Push and open the PR only after the owner's OK. Suggested title: `fix(wt): an omlx model that is loading is not a loaded one (#259)`. The body lists the table "Decisions This Plan Makes" rows for #259, states that `wt stop` on a loading model is unchanged (omlx refuses the unload), and ends with `Closes #259`.

---

## PR C — #258, show what a start unloaded

When a pool start makes omlx unload another model, `lifecycle.reconcilePool` prints `wt: omlx unloaded <id> to make room` through `routePrintf` (`lifecycle.go:344`) and calls `opts.OnUnloaded(en)` (`:348`). The TUI start flow passes no `OnUnloaded` (`start_flow.go:291-299`) and the line goes to stderr, under the alt screen. The route is removed correctly; only the message is lost.

Create the branch from the monorepo root:

```bash
git fetch origin
git switch -c fix/258-tui-shows-unloaded-models origin/main
```

### Task 7: The start flow reports what omlx unloaded

**Files:**
- Modify: `wt/internal/tui/start_flow.go:13-17` (imports), `:262-265` (`startDoneMsg`), `:282-303` (`runStart`), `:389-427` (`finishStart`)
- Modify: `wt/internal/tui/app.go:94` (`model`), `:1104-1114` (`launchSelected`)
- Create: `wt/internal/tui/start_unloaded_test.go`
- Modify: `wt/docs/internals/tui.md`, `wt/docs/wt-start-stop.md`, `wt/CHANGELOG.md`

**Interfaces:**
- Consumes (existing):
  - `lifecycle.Options.OnUnloaded func(localmodels.Entry)` (`lifecycle.go:45`): called once per model a pool start unloaded, on the goroutine that runs the start, before the start returns, whether or not it succeeded
  - `func (m *model) recordRouteNotes(notes string)` (`start_flow.go:179`): appends to `pendingRouteNotes`, printed on the real terminal when the alt screen is released
  - `func lifecycle.StartErrorMessage(id string, err error) string`
  - test helpers in `internal/tui`: `startFixture(t, provider, id, name) model`, `enterStartRow(t, m, id) (model, tea.Cmd)`, `stubStartModel(t, behavior) *startCalls`, `recvStart(t, m) tea.Msg`, `updateMsg(m, msg) (model, tea.Cmd)`, `drainCmds(t, m, cmd) model`, `stubRouteNotes(t)`
- Produces:
  - `startDoneMsg.unloaded []string`
  - `func unloadedNote(ids []string) string`: `"omlx unloaded A, B to make room"`, or `""` for no ids
  - `func withNote(note, status string) string`: `note + "; " + status`, or `status` alone when `note` is empty
  - `model.startNote string`: the note of a start that succeeded, set by `finishStart` and taken (and cleared) by `launchSelected`, which puts it ahead of `launch failed: …`

- [ ] **Step 1: Write the failing tests**

Create `wt/internal/tui/start_unloaded_test.go`:

```go
package tui

import (
	"context"
	"errors"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/ohanaverse/local-ai-setup/wt/internal/lifecycle"
	"github.com/ohanaverse/local-ai-setup/wt/internal/localmodels"
)

// unloadingStart scripts a start that makes omlx unload ids before it ends
// with err, reporting each the way the engine does: through OnUnloaded. A flow
// that passes no OnUnloaded is told nothing, as the real engine would tell it
// nothing, so its tests fail on what the user sees rather than on a nil call.
func unloadingStart(err error, ids ...string) func(int, context.Context, lifecycle.Target, lifecycle.Options) error {
	return func(_ int, _ context.Context, _ lifecycle.Target, opts lifecycle.Options) error {
		for _, id := range ids {
			if opts.OnUnloaded != nil {
				opts.OnUnloaded(localmodels.Entry{ProviderID: "omlx", ModelID: id})
			}
		}
		return err
	}
}

// TestFailedStartShowsWhatOmlxUnloaded pins #258. omlx can unload a model to
// make room and then fail the load. The engine's "omlx unloaded X" line goes
// to stderr, which the alt screen hides, so the picker came back showing only
// the failure: a model another session was using was gone and nothing said so.
// The status line now names every unloaded model, ahead of the failure.
func TestFailedStartShowsWhatOmlxUnloaded(t *testing.T) {
	stubRouteNotes(t)
	m := startFixture(t, "omlx", "omlx/qwen3.8", "qwen3.8")
	stubStartModel(t, unloadingStart(errors.New("boom"), "omlx/old", "omlx/older"))
	got, _ := enterStartRow(t, m, "omlx/qwen3.8")
	got, _ = updateMsg(got, recvStart(t, got))
	if want := "omlx unloaded omlx/old, omlx/older to make room; failed to start omlx/qwen3.8: boom"; got.status != want {
		t.Errorf("status = %q\nwant     %q", got.status, want)
	}
	if got.phase != phaseModel {
		t.Errorf("phase = %v, want the picker", got.phase)
	}
	if pendingRouteNotes != "" {
		t.Errorf("route notes = %q, want none: the picker's status line already says it", pendingRouteNotes)
	}
}

// TestStartThatUnloadedNothingLeavesTheStatusAlone verifies the note appears
// only when there is something to say: an ordinary failed start keeps exactly
// the status it had, and an ordinary successful one prints nothing extra.
func TestStartThatUnloadedNothingLeavesTheStatusAlone(t *testing.T) {
	stubRouteNotes(t)
	m := startFixture(t, "omlx", "omlx/qwen3.8", "qwen3.8")
	stubStartModel(t, unloadingStart(errors.New("boom")))
	got, _ := enterStartRow(t, m, "omlx/qwen3.8")
	got, _ = updateMsg(got, recvStart(t, got))
	if want := "failed to start omlx/qwen3.8: boom"; got.status != want {
		t.Errorf("status = %q, want %q", got.status, want)
	}

	m = startFixture(t, "omlx", "omlx/qwen3.8", "qwen3.8")
	m.agent = "not-a-real-agent" // the launch after the start fails, observably
	stubStartModel(t, unloadingStart(nil))
	got, _ = enterStartRow(t, m, "omlx/qwen3.8")
	_, _ = updateMsg(got, recvStart(t, got))
	if pendingRouteNotes != "" {
		t.Errorf("route notes = %q, want none", pendingRouteNotes)
	}
}

// TestCancelledStartShowsWhatOmlxUnloaded verifies a start the user cancelled
// still reports what omlx unloaded before the cancel took effect: the load is
// what evicts, so cancelling it does not bring the evicted model back.
func TestCancelledStartShowsWhatOmlxUnloaded(t *testing.T) {
	m := startFixture(t, "omlx", "omlx/qwen3.8", "qwen3.8")
	stubStartModel(t, func(_ int, ctx context.Context, _ lifecycle.Target, opts lifecycle.Options) error {
		<-ctx.Done()
		return unloadingStart(ctx.Err(), "omlx/old")(0, ctx, lifecycle.Target{}, opts)
	})
	got, _ := enterStartRow(t, m, "omlx/qwen3.8")
	got, _ = updateMsg(got, tea.KeyMsg{Type: tea.KeyEsc})
	got, _ = updateMsg(got, recvStart(t, got))
	if want := "omlx unloaded omlx/old to make room; cancelled"; got.status != want {
		t.Errorf("status = %q, want %q", got.status, want)
	}
}

// TestSuccessfulStartPrintsWhatOmlxUnloadedAboveTheAgent verifies the note
// after a start that succeeded. The agent launches at once, so the line joins
// the route notes, which are printed on the real terminal the moment the alt
// screen is released: above the agent's output. When that launch fails the
// picker comes back instead, and the note must be on its status line too,
// ahead of the failure — otherwise the user sees only "launch failed" until wt
// exits. Here the launch is made to fail (an agent no driver knows), so both
// are checked.
func TestSuccessfulStartPrintsWhatOmlxUnloadedAboveTheAgent(t *testing.T) {
	stubRouteNotes(t)
	m := startFixture(t, "omlx", "omlx/qwen3.8", "qwen3.8")
	m.agent = "not-a-real-agent"
	stubStartModel(t, unloadingStart(nil, "omlx/old"))
	got, _ := enterStartRow(t, m, "omlx/qwen3.8")
	got, _ = updateMsg(got, recvStart(t, got))
	if want := "wt: omlx unloaded omlx/old to make room\n"; pendingRouteNotes != want {
		t.Errorf("route notes = %q, want %q", pendingRouteNotes, want)
	}
	if want := "omlx unloaded omlx/old to make room; launch failed: "; !strings.HasPrefix(got.status, want) {
		t.Errorf("status = %q, want it to begin %q", got.status, want)
	}
}

// TestUnloadedNoteSurvivesALongFailureAtEightyColumns verifies the note is
// still on screen when the failure that follows it is longer than the
// terminal. The status line is one line cut at the terminal's width, and
// omlx's own refusal text runs to several hundred characters: with the note
// after it, the one fact the user cannot find anywhere else would be the part
// cut off.
func TestUnloadedNoteSurvivesALongFailureAtEightyColumns(t *testing.T) {
	m := startFixture(t, "omlx", "omlx/qwen3.8", "qwen3.8")
	stubStartModel(t, unloadingStart(errors.New(strings.Repeat("omlx says no. ", 40)), "omlx/old"))
	got, _ := enterStartRow(t, m, "omlx/qwen3.8")
	got, cmd := updateMsg(got, recvStart(t, got))
	got = drainCmds(t, got, cmd)
	view := got.View()
	if !strings.Contains(view, "omlx unloaded omlx/old to make room") {
		t.Errorf("the 80-column picker does not show the note:\n%s", view)
	}
	for _, line := range strings.Split(view, "\n") {
		if w := lipgloss.Width(line); w > 80 {
			t.Errorf("line is %d columns wide, want at most 80: %q", w, line)
		}
	}
}

// TestUnloadedNoteWithRealModelIDsAtEightyColumns pins the price of putting the
// note first. With ids as long as real ones, the note and "failed to start
// <id>: " already fill an 80-column status line, so the reason for the failure
// is what gets cut. That is the accepted trade: the unloaded model is the fact
// found nowhere else, and `wt start <id>` on the command line prints the
// reason in full. What must hold is that the note is whole, the line still
// says the start failed, and nothing overflows.
func TestUnloadedNoteWithRealModelIDsAtEightyColumns(t *testing.T) {
	m := startFixture(t, "omlx", "omlx/Ornith-1.5-35B-6bit", "Ornith-1.5-35B-6bit")
	stubStartModel(t, unloadingStart(errors.New("boom"), "omlx/Qwen3.8-27B-4bit"))
	got, _ := enterStartRow(t, m, "omlx/Ornith-1.5-35B-6bit")
	got, cmd := updateMsg(got, recvStart(t, got))
	got = drainCmds(t, got, cmd)
	view := got.View()
	if !strings.Contains(view, "omlx unloaded omlx/Qwen3.8-27B-4bit to make room; failed to start") {
		t.Errorf("the 80-column picker does not show the note and that the start failed:\n%s", view)
	}
	for _, line := range strings.Split(view, "\n") {
		if w := lipgloss.Width(line); w > 80 {
			t.Errorf("line is %d columns wide, want at most 80: %q", w, line)
		}
	}
}
```

- [ ] **Step 2: Run them and confirm they fail**

Run: `go test ./internal/tui -run 'Unloaded' -v`
Expected: FAIL. `TestFailedStartShowsWhatOmlxUnloaded` reports `status = "failed to start omlx/qwen3.8: boom"`; `TestCancelledStartShowsWhatOmlxUnloaded` reports `status = "cancelled"`; `TestSuccessfulStartPrintsWhatOmlxUnloadedAboveTheAgent` reports `route notes = ""` and `status = "launch failed: …", want it to begin "omlx unloaded omlx/old to make room; launch failed: "`; `TestUnloadedNoteSurvivesALongFailureAtEightyColumns` and `TestUnloadedNoteWithRealModelIDsAtEightyColumns` each print a picker with no note. `TestStartThatUnloadedNothingLeavesTheStatusAlone` passes already: it guards the unchanged case.

- [ ] **Step 3: Carry the unloaded ids in the done message**

In `start_flow.go`, add the import, so the block's last two lines read:

```go
	"github.com/ohanaverse/local-ai-setup/wt/internal/lifecycle"
	"github.com/ohanaverse/local-ai-setup/wt/internal/localmodels"
)
```

Replace `startDoneMsg`:

```go
type startDoneMsg struct {
	id  int
	err error
}
```

with:

```go
type startDoneMsg struct {
	id  int
	err error
	// unloaded is the ids of the models omlx unloaded to make room for this
	// start (lifecycle.Options.OnUnloaded), set whether or not it succeeded.
	unloaded []string
}
```

Replace the whole of `runStart` (its comment and body) with:

```go
// runStart runs the engine in a goroutine and streams its stages, then its
// result, on the returned channel (closed after the result). The startModel
// seam is read HERE, on the caller's goroutine, so a test seam swap never
// races the goroutine's late first read.
func runStart(ctx context.Context, cfg *config.Config, t lifecycle.Target, allow bool, id int) <-chan tea.Msg {
	ch := make(chan tea.Msg, 16)
	start := startModel
	go func() {
		defer close(ch)
		// The engine calls OnUnloaded on this goroutine, before start returns,
		// so the slice needs no lock: it is complete when the done message
		// carries it to the update goroutine.
		var unloaded []string
		err := start(ctx, cfg, t, lifecycle.Options{
			AllowReplace: allow,
			Progress: func(s lifecycle.Stage) {
				select {
				case ch <- startStageMsg{id: id, stage: s}:
				case <-ctx.Done():
				}
			},
			OnUnloaded: func(en localmodels.Entry) { unloaded = append(unloaded, en.ModelID) },
		})
		ch <- startDoneMsg{id: id, err: err, unloaded: unloaded}
	}()
	return ch
}
```

- [ ] **Step 4: Show the note on every way out of the start**

In `start_flow.go`, replace the whole of `finishStart` (lines 389-427) with the following, which adds `unloadedNote` and `withNote` above it:

```go
// unloadedNote words what a pool start unloaded: "omlx unloaded A, B to make
// room", or "" when it unloaded nothing. The engine prints the same sentence
// per model on stderr, which the alt screen hides (#258).
func unloadedNote(ids []string) string {
	if len(ids) == 0 {
		return ""
	}
	return "omlx unloaded " + strings.Join(ids, ", ") + " to make room"
}

// withNote puts note ahead of status, joined with "; ". The note goes first
// because the status line is one line, cut at the terminal's width, and the
// text after it has no fixed length.
func withNote(note, status string) string {
	if note == "" {
		return status
	}
	return note + "; " + status
}

func (m model) finishStart(msg startDoneMsg) (model, tea.Cmd) {
	st := m.start
	m.start = nil
	st.cancel()
	// omlx can unload a model and then fail the load, or be cancelled, so the
	// note is shown on every way out, not only after a success.
	note := unloadedNote(msg.unloaded)
	back := func(status string) (model, tea.Cmd) {
		m.status = withNote(note, status)
		m.phase = phaseModel
		// The table was built before the attempt and a start can have stopped
		// the occupant and then failed, so re-probe instead of showing stale
		// RUNNING.
		return m.refreshTable()
	}
	if st.cancelling {
		return back("cancelled")
	}
	if msg.err == nil {
		m.phase = phaseModel
		// The launch that follows routes through LiteLLM: settle the route
		// hook's async proxy restart before handing the model to an agent.
		waitPendingRoutes()
		// The launch follows a successful start, and proceedToLaunch clears
		// the status line, so the note takes the route notes' way out: onto
		// the real terminal, above the agent's output. If that launch fails
		// the picker comes back instead, so launchSelected is also handed the
		// note and puts it ahead of the failure on the status line.
		if note != "" {
			m.recordRouteNotes("wt: " + note + "\n")
			m.startNote = note
		}
		return m.proceedToLaunch()
	}
	var occ *lifecycle.OccupiedError
	var unk *lifecycle.OccupancyUnknownError
	title := ""
	switch {
	case errors.As(msg.err, &occ):
		title = fmt.Sprintf("Starting %s will stop %s", st.item.model.ID, strings.Join(occ.IDs(), ", "))
	case errors.As(msg.err, &unk):
		title = fmt.Sprintf("Cannot tell whether %s at %s is already serving a model", unk.ProviderID, unk.Origin)
	default:
		return back(lifecycle.StartErrorMessage(st.item.model.ID, msg.err))
	}
	choices := list.New(buildReplaceChoices(), ThemedListDelegate(m.theme), m.width-2, m.height-2)
	choices.Title = title
	m.replace = &replaceState{item: st.item, choices: choices}
	m.phase = phaseReplaceConfirm
	return m, nil
}
```

The two replace-confirm cases need no note: `*OccupiedError` and `*OccupancyUnknownError` are returned before anything is loaded, so nothing was unloaded.

- [ ] **Step 5: Put the note ahead of a launch failure**

A start that succeeded is followed by the launch, and the launch can fail: `proceedToLaunch` clears the status line and, for a row this flow just started, calls `launchSelected` directly, whose failure returns to the picker with `launch failed: …`. That picker must carry the note. (`app.go` has two more `launch failed` sites, in `launchCommand` and `launchPassthrough`; neither has a model layer, so no start leads to them and they stay as they are.)

In `app.go`, in `type model struct`, after the `launchModel` field:

```go
	launchModel  config.Model // the model being launched, captured when the launch began (the routing phase finishes an Update later)
```

add:

```go
	startNote    string       // what the start that led to this launch made omlx unload (unloadedNote, #258); launchSelected puts it ahead of a launch failure
```

In `launchSelected`, replace:

```go
	cmd, err := launchAgent(m.agent, m.launchModel, m.selectedPath, m.yolo, m.cfg, m.extraArgs)
	if err != nil {
		m.status = "launch failed: " + err.Error()
		return m.refreshTable()
	}
```

with:

```go
	// The note of the start that led here (#258), taken once: a later launch
	// from the same picker is not about that start.
	note := m.startNote
	m.startNote = ""
	cmd, err := launchAgent(m.agent, m.launchModel, m.selectedPath, m.yolo, m.cfg, m.extraArgs)
	if err != nil {
		m.status = withNote(note, "launch failed: "+err.Error())
		return m.refreshTable()
	}
```

The quoted `launchAgent` call appears once in `app.go` with `m.launchModel`; `launchCommand`'s call passes `config.Model{}` and is not the one to change.

- [ ] **Step 6: Run the tests and confirm they pass**

Run: `go test ./internal/tui -run 'Unloaded' -v`
Expected: PASS, six tests.

Run: `go test -race ./internal/tui -run 'Unloaded|TestStart|TestSuccessfulStart'`
Expected: `ok`, no race reported.

Run: `go test ./...`
Expected: every line starts with `ok`.

- [ ] **Step 7: Update the docs**

In `wt/docs/internals/tui.md`, in the paragraph that begins ``Start-on-select (`start_flow.go`)`` (line 9), append after its last sentence (`The single-row auto-launch shortcut applies only to launch rows.`):

```markdown
 The flow passes `lifecycle.Options.OnUnloaded` and carries the ids in `startDoneMsg.unloaded` (#258): on a failed or cancelled start the status line reads `omlx unloaded <ids> to make room; <failure>` — the note first, because the line is cut at the terminal's width — and after a successful start, where the launch follows, `wt: omlx unloaded <ids> to make room` joins `pendingRouteNotes` and is printed above the agent's output. If that launch fails the picker comes back, so `finishStart` also leaves the note in `model.startNote` and `launchSelected` puts it ahead of `launch failed: …` (`withNote`). With long model ids the note and `failed to start <id>: ` can fill an 80-column line and the failure's reason is cut; `wt start <id>` prints it in full.
```

In `wt/docs/wt-start-stop.md`, in the section "omlx: a pool of loaded models", after the bullet that ends `as too large fails with omlx's own explanation of what holds the memory.`, insert:

```markdown
- In the `wt` picker the unloaded models are reported without the stderr
  line, which the full-screen picker hides, and without the
  `(not predicted)` marker: after a start that fails or is cancelled the
  status line begins `omlx unloaded <id> to make room`, and after a start
  that succeeds `wt: omlx unloaded <id> to make room` is printed above the
  agent's output. If the agent then fails to launch, the picker's status
  line begins with the same note, ahead of `launch failed`.
```

In `wt/CHANGELOG.md`, under `## Unreleased`, add as the first bullet of `### Fixed`:

```markdown
- The `wt` picker says which omlx models a start unloaded (#258). The line
  was printed only on stderr, which the full-screen picker hides, so a model
  another session was using could go without a word. It is now on the
  picker's status line after a failed or cancelled start, and above the
  agent's output after a successful one (and on the status line again if
  the agent then fails to launch).
```

- [ ] **Step 8: Check and commit**

Run from `wt/`:

```bash
gofmt -l .
go build ./...
go vet ./...
go test ./...
make check
```

Expected: `gofmt -l .` prints nothing; every `go test` line starts with `ok`; `make check` passes.

Run from the monorepo root: `make lint`
Expected: shell lint passes and the link check prints `ALL LINKS OK`.

```bash
git add wt/internal/tui/start_flow.go wt/internal/tui/app.go wt/internal/tui/start_unloaded_test.go wt/docs wt/CHANGELOG.md
git commit -m "fix(wt): the TUI start flow says which models omlx unloaded (#258)"
```

- [ ] **Step 9: Verify as CI does, then hand off**

Run from the monorepo root: `make test-all`
Expected: lint passes, the modelman suite passes, and the wt lines all start with `ok`.

Stop here. Tell the owner the branch is ready and what `make test-all` printed. Push and open the PR only after their OK. Suggested title: `fix(wt): the TUI start flow says which models omlx unloaded (#258)`. The body ends with `Closes #258` and states the decisions from "Decisions This Plan Makes": after a successful start the note goes above the agent's output, and onto the status line if the launch then fails; the note comes first, so with long model ids the failure's reason can be cut at 80 columns; and "The TUI does not mark unpredicted evictions; #258's 'not predicted' motivation is met only by naming every unloaded model."

---

### Task 8: Check #258 against a real omlx (needs the owner)

The fake starts cover every path of Task 7. This task shows the note once on a real screen. It needs omlx to unload one model for another, which on a machine with room for both means lowering omlx's memory ceiling for the run. That edits the owner's `~/.omlx/settings.json` and restarts omlx, so **do nothing here until the owner says to**, and never without the redirects in Step 2. If the owner would rather not, report that #258 is covered by the fake-start tests only; that is an acceptable end to this task.

**Files:** none.

**Interfaces:**
- Consumes: PR C, merged or built locally from its branch.
- Produces: a short report for the owner.

- [ ] **Step 1: Ask the owner**

Ask and wait for the answers:

1. Which two omlx models to use, and their sizes. The first is loaded and then unloaded; the second is the one started.
2. Whether omlx's ceiling may be lowered for this run. In omlx 0.7.0 the setting is `memory.memory_guard_tier` set to `custom` with `memory.memory_guard_custom_ceiling_gb` in `~/.omlx/settings.json`, read when the server starts. The names come from omlx's source and the owner's settings file; this plan did not exercise them, so confirm with the owner that this is how they lower it and how they restart omlx (`omlx restart`, or their own way).
3. Which agent to pick in the picker, and how to leave it at once (its exit command).

- [ ] **Step 2: Build the binary and redirect its config**

Run from `wt/`, on the PR C branch or on `main` after PR C merged:

```bash
go build -o /tmp/wt-verify ./cmd/wt
```

Then, in the terminal used below:

```bash
export XDG_CONFIG_HOME="$(mktemp -d)"
mkdir -p "$XDG_CONFIG_HOME/local-ai" "$XDG_CONFIG_HOME/agent-wt"
cp ~/.config/local-ai/registry.toml "$XDG_CONFIG_HOME/local-ai/"
cp ~/.config/agent-wt/config.toml "$XDG_CONFIG_HOME/agent-wt/"
cp ~/.config/litellm/config.yaml "$XDG_CONFIG_HOME/litellm-config.yaml"
export WT_LITELLM_CONFIG="$XDG_CONFIG_HOME/litellm-config.yaml"
export WT_LITELLM_RESTART_CMD=true
```

- [ ] **Step 3: Lower the ceiling**

Choose a ceiling, in GB, larger than either model and smaller than the two together, and put it in `CEILING_GB`:

```bash
cp ~/.omlx/settings.json ~/.omlx/settings.json.wt-verify-bak
CEILING_GB=<the number chosen> python3 - <<'PY'
import json, os
path = os.path.expanduser("~/.omlx/settings.json")
settings = json.load(open(path))
settings["memory"]["memory_guard_tier"] = "custom"
settings["memory"]["memory_guard_custom_ceiling_gb"] = float(os.environ["CEILING_GB"])
json.dump(settings, open(path, "w"), indent=2)
PY
omlx restart
```

- [ ] **Step 4: Start the second model from the picker**

Load the first model:

```bash
/tmp/wt-verify start omlx/<first>
```

Then, in a real terminal resized to 80x24, from the monorepo root, run `/tmp/wt-verify` with no arguments. Pick the current worktree, the agent the owner named, and the row for `omlx/<second>`; choose "Replace and start" when asked.

Expected, one of two, and both pass:

- The agent launches, and `wt: omlx unloaded omlx/<first> to make room` is printed above its output. Leave the agent at once with its exit command.
- The launch fails and the picker comes back with a status line that begins `omlx unloaded omlx/<first> to make room; launch failed:`. This is likely here: the route for the started model was written to the scratch `config.yaml` and the real proxy was never restarted (`WT_LITELLM_RESTART_CMD=true`), so a launch through LiteLLM can be refused. Quit the picker with `q`; the `wt: omlx unloaded …` line is then printed on the terminal.

Either way, check only that the note names `omlx/<first>`.

- [ ] **Step 5: Restore and clean up**

```bash
/tmp/wt-verify stop omlx/<second> --yes
mv ~/.omlx/settings.json.wt-verify-bak ~/.omlx/settings.json
omlx restart
rm -rf "$XDG_CONFIG_HOME" /tmp/wt-verify
unset XDG_CONFIG_HOME WT_LITELLM_CONFIG WT_LITELLM_RESTART_CMD
```

If the owner's omlx was not running before this task, stop it instead of restarting it (`wt stop omlx`). Then, with the real environment, confirm the real routes were never touched:

```bash
wt litellm sync --dry-run
```

Expected: no route to add or remove. If it lists a change, tell the owner what it lists; do not run the real sync without their OK.

- [ ] **Step 6: Report**

Tell the owner which of Step 4's two outcomes was seen and the exact line, or that the task was not run and #258 is covered by the fake-start tests only.
