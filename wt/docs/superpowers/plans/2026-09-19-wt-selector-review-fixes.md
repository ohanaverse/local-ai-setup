# wt Selector Table — Code-Review Fixes Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Fix the nine findings from the `/code-review` pass over the `wt-selector` branch, in priority order, without regressing the selector-table behavior the branch shipped.

**Architecture:** The one functional regression (finding #1) is fixed at its source: `localmodels` learns to distinguish "the probe says the model isn't there" from "the probe could not tell", and `internal/tui` consumes that distinction as a third STATUS value instead of collapsing both into `absent`. The remaining findings are a merge-corrupted changelog, a redundant probe with a comment that justifies it with a false reason, dead code, and doc drift.

**Tech Stack:** Go 1.26.7 (module root `wt/`), `github.com/charmbracelet/bubbletea` + `bubbles/list`, `net/http/httptest` for probe fixtures, `go test`.

**Spec:** `docs/superpowers/specs/2026-09-19-wt-model-selector-table-design.md` (the table this branch built) and `docs/superpowers/specs/2026-09-19-wt-local-model-inventory-design.md` (the inventory whose `Entry` gains a field). Both are dated records and are **not** edited by this plan; where this plan extends the selector spec's STATUS vocabulary, Task 3's changelog entry and Task 8's living docs carry the change.

**Source of findings:** the `/code-review` pass over `git diff main...HEAD` on branch `wt-selector`.

## Global Constraints

- All Go commands run from the `wt/` module root, not the worktree root: `cd wt && go build ./... && go vet ./... && go test ./...`.
- Every `Test*` needs a top-level `//` comment stating **what** it tests and **why** it matters (the user-facing consequence of a regression). This is enforced by convention in `wt/CLAUDE.md`, not by tooling — a reviewer will reject a bare test.
- Tests must never probe live providers. `internal/tui`'s `TestMain` stubs `runInventory` to a no-op; `internal/localmodels` tests use `httptest` only.
- Prefer asserting on unexported functions directly. Do **not** parse rendered lipgloss output — it couples tests to border glyphs and flakes under forced-color ANSI. (`modeltable_test.go` asserts on `tableRow`/`modelItem` fields and on the header string; follow that pattern.)
- `gofmt` is a CI gate (`make check` → `go-format-check`). Run `gofmt -l .` from `wt/` before each commit; it must print nothing.
- The identifier `ArtifactKnown` is load-bearing across Task 1 and Task 2 — do not rename it in one place only.
- Commit messages: a scope tag plus a `completes plan item #N` reference for this plan's items, ending with the attribution line `Co-Authored-By: Claude Code <noreply@anthropic.com>`.
- **Never** create a PR or push a branch without asking the user first (`wt/CLAUDE.md` / user global instructions).
- Dated `docs/superpowers/specs/**` and `docs/superpowers/plans/**` files are historical records: do not edit `2026-09-19-wt-model-selector-table-design.md`, `2026-09-19-wt-model-selector-table.md`, or `2026-09-19-wt-local-model-inventory-design.md`. Living docs — `docs/guides/**`, `wt/CLAUDE.md`, `wt/CHANGELOG.md` — **are** updated (Tasks 3 and 8).
- No `modelman.toml`/`registry.toml` state changes in this plan, so the `git grep -n "exposed = " docs/guides/` drift check is not triggered.

---

### Task 1: `localmodels.Entry.ArtifactKnown` — "not there" vs "couldn't tell"

Finding #1, first half. The inventory currently encodes three different situations as one value: `Artifact == ""`. It means "the provider answered and doesn't have this model", "the probe failed", or "this family's artifacts aren't discoverable at all". Only the first is knowledge; the other two are ignorance. This task teaches `localmodels` to say which.

**Files:**
- Modify: `wt/internal/localmodels/inventory.go` (the `Entry` struct at ~44-51; a new `source` method near `matchArtifact` at ~88; the registered-model loop at ~274-291; the discovered-entry literal at ~298-303)
- Test: `wt/internal/localmodels/inventory_test.go`

**Interfaces:**
- Consumes: nothing from earlier tasks.
- Produces: `Entry.ArtifactKnown bool` — read by Task 2's `buildRows`. `(*source).knowsArtifacts() bool` — package-private.

- [ ] **Step 1: Write the failing test**

Append to `wt/internal/localmodels/inventory_test.go` (it already imports `config`, `httptest`, and has the `ollamaServer`, `modelsServer`, `localProvider`, `byModelID` helpers):

```go
// TestInventoryArtifactKnownReflectsProbeOutcome verifies ArtifactKnown
// distinguishes "the provider answered and does not have this model" from "the
// probe could not tell". An unreachable family and mlx_lm_server (which cannot
// enumerate artifacts at all) must both report false, while a probe that
// answered reports true even when it found nothing. A consumer that reads an
// empty Artifact as "missing" without this flag hides every configured ollama
// model behind a transient daemon hiccup.
func TestInventoryArtifactKnownReflectsProbeOutcome(t *testing.T) {
	// The ollama daemon is down: nothing was discovered, and that is NOT the
	// same as "the model is not pulled".
	dead := ollamaServer(t, nil, nil)
	deadURL := dead.URL
	dead.Close()
	down := inventory(&config.Config{
		Providers: []config.Provider{localProvider("ollama", deadURL, "")},
		Models:    []config.Model{{ID: "ollama/x:1b", ProviderID: "ollama", ModelName: "x:1b"}},
	}, testClient)
	if st := down.Providers["ollama"]; st != StatusUnreachable {
		t.Fatalf("probe status = %q, want unreachable", st)
	}
	if e, ok := byModelID(down, "ollama/x:1b"); !ok || e.ArtifactKnown {
		t.Errorf("unreachable ollama entry = %+v ok=%v, want ArtifactKnown=false", e, ok)
	}

	// The probe DID answer and found nothing: the model is genuinely absent.
	up := inventory(&config.Config{
		Providers: []config.Provider{localProvider("ollama", ollamaServer(t, []string{"other:1b"}, nil).URL, "")},
		Models:    []config.Model{{ID: "ollama/gone:7b", ProviderID: "ollama", ModelName: "gone:7b"}},
	}, testClient)
	if e, ok := byModelID(up, "ollama/gone:7b"); !ok || !e.ArtifactKnown || e.Artifact != "" {
		t.Errorf("answered entry = %+v ok=%v, want ArtifactKnown=true with empty Artifact", e, ok)
	}

	// mlx_lm_server serves one target+draft pairing per process and wt cannot
	// reconstruct the served name, so its registered rows are always unknown.
	mlx := inventory(&config.Config{
		Providers: []config.Provider{localProvider("mlx_lm_server", modelsServer(t, "/some/target/path").URL, "")},
		Models:    []config.Model{{ID: "mlx_lm_server/x", ProviderID: "mlx_lm_server", ModelName: "x"}},
	}, testClient)
	if e, ok := byModelID(mlx, "mlx_lm_server/x"); !ok || e.ArtifactKnown {
		t.Errorf("mlx_lm_server entry = %+v ok=%v, want ArtifactKnown=false", e, ok)
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `cd wt && go test ./internal/localmodels -run TestInventoryArtifactKnownReflectsProbeOutcome -v`
Expected: FAIL to build — `e.ArtifactKnown undefined (type Entry has no field or method ArtifactKnown)`.

- [ ] **Step 3: Write the minimal implementation**

In `wt/internal/localmodels/inventory.go`, add the field to `Entry` (keep the existing field comments):

```go
// Entry is one local model: registered (Registered) or discovered.
type Entry struct {
	ProviderID string // registry provider id (omlx-6bit rows keep theirs); the family id for discovered entries
	Artifact   string // pulled/on-disk name in the provider's spelling; "" for a registered model not found on disk
	ModelID    string // registry id when registered, else config.DiscoveredModelID(ProviderID, Artifact)
	Registered bool
	Running    bool // serving right now (live probe only)
	// ArtifactKnown reports whether the probe actually determined this entry's
	// artifact presence. False means unknown, NOT missing: the family's
	// discovery failed (StatusUnreachable, so artifacts was never populated) or
	// the family cannot enumerate artifacts at all (mlx_lm_server). A consumer
	// must not read a false ArtifactKnown plus an empty Artifact as "the model
	// isn't there" — a transient probe failure would then hide a model that is
	// pulled and launchable.
	ArtifactKnown bool
}
```

Add the `source` method next to `matchArtifact`:

```go
// knowsArtifacts reports whether this family's probe could enumerate what is
// pulled or on disk. False for mlx_lm_server (one target+draft pairing per
// process, and the served name is not reconstructable) and for a probe that
// failed outright (StatusUnreachable), in which case artifacts was never
// populated — so an empty Artifact carries no information either way.
func (s *source) knowsArtifacts() bool {
	return s.family != "mlx_lm_server" && s.status != StatusUnreachable
}
```

In the registered-model loop, set the field from the family's probe result:

```go
		e := Entry{ProviderID: m.ProviderID, ModelID: m.ID, Registered: true}
		if src := sources[familyOf(m.ProviderID)]; src != nil {
			e.ArtifactKnown = src.knowsArtifacts()
			for _, a := range src.artifacts {
```

(Leave the rest of that block — `consumed`, `matchArtifact`, the `name`/`isRunning` fallback — unchanged.)

In the discovered-entry literal, mark presence as known, since the artifact is the thing being iterated:

```go
			snap.Entries = append(snap.Entries, Entry{
				ProviderID:    f,
				Artifact:      a,
				ModelID:       config.DiscoveredModelID(f, a),
				Running:       src.isRunning(a),
				ArtifactKnown: true,
			})
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `cd wt && go test ./internal/localmodels -v`
Expected: PASS, including the pre-existing `TestInventoryProviderDownDoesNotFailOthers`, `TestInventoryMlxLMServerRunningNoDiscovery`, and `TestInventoryRegisteredMissingFromDisk`. Also run `gofmt -l .` from `wt/` — it must print nothing.

- [ ] **Step 5: Commit**

```bash
git add wt/internal/localmodels/inventory.go wt/internal/localmodels/inventory_test.go
git commit -m "feat(localmodels): add Entry.ArtifactKnown to separate absent from unknown - completes plan item #1

Co-Authored-By: Claude Code <noreply@anthropic.com>"
```

---

### Task 2: `statusUnknown` in the selector table

Finding #1, second half — the user-visible fix. `buildRows` currently reads an empty `Artifact` as `statusAbsent` in two extra situations (no registered entry at all, and every `mlx_lm_server` row), and `launchable()` refuses any `statusAbsent` ollama row. Net effect: a transient ollama probe failure made every configured ollama model unlaunchable in the TUI while `-M` and the non-TUI path launched the same model fine, and a *running* `mlx_lm_server` row rendered `STATUS absent` beside `RUNNING run` for its entire life.

**Files:**
- Modify: `wt/internal/tui/modelrows.go` (the `rowStatus` consts at 14-20; `buildRows`' local branch at 70-80; the `tableInput.inventory` doc comment at 36-37)
- Modify: `wt/internal/tui/modeltable.go` (STATUS column width: the `famW, idW, costW…` declaration at 71, the measure loop at 72-86, and the two `padRunes(…, 6)` calls at 89 and 101)
- Test: `wt/internal/tui/modelrows_test.go`, `wt/internal/tui/modeltable_test.go`

**Interfaces:**
- Consumes: `localmodels.Entry.ArtifactKnown` from Task 1.
- Produces: `statusUnknown rowStatus = "unknown"` — the third status value. No signature changes.

- [ ] **Step 1: Write the failing tests**

In `wt/internal/tui/modelrows_test.go`, the existing `TestBuildRowsMarksAbsentAndRespectsAgentAndFilters` asserts `rows[0].status == statusAbsent` for a registered entry the probe *did* answer about — so its fixture must now say so. Change that one entry to carry the flag (this keeps the test's meaning: the probe ran, the artifact is not there):

```go
	inv := &localmodels.Snapshot{Entries: []localmodels.Entry{
		{ProviderID: "omlx", ModelID: "omlx/gone", Registered: true, ArtifactKnown: true},
		{ProviderID: "mtplx", ModelID: "mtplx/x", Artifact: "x"},      // claude does not support mtplx
		{ProviderID: "ollama", ModelID: "omlx/gone", Artifact: "dup"}, // id collision with a row
		{ProviderID: "ollama", ModelID: "ollama/ok", Artifact: "ok"},
	}}
```

Then append two new tests:

```go
// TestBuildRowsUnknownLocalStatus verifies the three-way artifact rule: a probe
// that answered and found nothing reads "absent" (and blocks an ollama launch,
// since the daemon would not have the model), a probe that could not tell reads
// "unknown" (and must NOT block — the non-TUI path trusts an ollama flag through
// a probe failure, so a transient daemon hiccup must not make the TUI refuse to
// launch a model that is actually pulled), and a row serving right now never
// reads absent even when its artifact could not be resolved (mlx_lm_server,
// whose target+draft pairing is not discoverable).
func TestBuildRowsUnknownLocalStatus(t *testing.T) {
	cfg := rowsTestCfg()
	cfg.Providers = append(cfg.Providers, config.Provider{ID: "mlx_lm_server", Location: config.LocationLocal})
	models := []config.Model{
		{ID: "ollama/unknown", ProviderID: "ollama", ModelName: "unknown", Location: config.LocationLocal},
		{ID: "omlx/nope", ProviderID: "omlx", ModelName: "nope", Location: config.LocationLocal},
		{ID: "mlx_lm_server/serving", ProviderID: "mlx_lm_server", ModelName: "serving", Location: config.LocationLocal},
	}
	inv := &localmodels.Snapshot{
		Providers: map[string]localmodels.Status{
			"ollama": localmodels.StatusUnreachable, "omlx": localmodels.StatusOK,
		},
		Entries: []localmodels.Entry{
			{ProviderID: "ollama", ModelID: "ollama/unknown", Registered: true},
			{ProviderID: "omlx", ModelID: "omlx/nope", Registered: true, ArtifactKnown: true},
			{ProviderID: "mlx_lm_server", ModelID: "mlx_lm_server/serving", Registered: true, Running: true},
		},
	}
	rows := buildRows(tableInput{cfg: cfg, agent: "claude", models: models, inventory: inv, usage: usage.NewStoreAt(t.TempDir())})
	byID := map[string]tableRow{}
	for _, r := range rows {
		byID[r.model.ID] = r
	}
	if got := byID["ollama/unknown"].status; got != statusUnknown {
		t.Errorf("unreachable ollama status = %q, want unknown", got)
	}
	if !byID["ollama/unknown"].launchable() {
		t.Error("an ollama row whose probe failed must stay launchable (fail open)")
	}
	if got := byID["omlx/nope"].status; got != statusAbsent {
		t.Errorf("answered omlx status = %q, want absent", got)
	}
	if byID["omlx/nope"].launchable() {
		t.Error("an absent non-running omlx row must not launch")
	}
	if got := byID["mlx_lm_server/serving"].status; got != statusOK {
		t.Errorf("running mlx_lm_server status = %q, want ok", got)
	}
}

// TestBuildRowsLocalModelMissingFromSnapshot verifies a local registry model
// with no inventory entry at all (an unresolvable location, or a family wt has
// no probe for) reads "unknown" rather than "absent": nothing was discovered
// about it, so calling it missing would be a guess.
func TestBuildRowsLocalModelMissingFromSnapshot(t *testing.T) {
	cfg := rowsTestCfg()
	models := []config.Model{{ID: "omlx/unprobed", ProviderID: "omlx", ModelName: "unprobed", Location: config.LocationLocal}}
	inv := &localmodels.Snapshot{Providers: map[string]localmodels.Status{"omlx": localmodels.StatusOK}}
	rows := buildRows(tableInput{cfg: cfg, agent: "claude", models: models, inventory: inv, usage: usage.NewStoreAt(t.TempDir())})
	if rows[0].status != statusUnknown {
		t.Errorf("status = %q, want unknown", rows[0].status)
	}
}
```

In `wt/internal/tui/modeltable_test.go`, append a test that pins the column width (`tableTestRows()` is defined at the top of that file; index 3 is the absent `omlx/gone` row):

```go
// TestRenderTableUnknownStatusAligns verifies a 7-rune "unknown" cell widens the
// STATUS column for the whole table rather than pushing every later column out
// of line — the header and the rows must agree, or RUNNING and COST start
// rendering under the wrong headings.
func TestRenderTableUnknownStatusAligns(t *testing.T) {
	rows := tableTestRows()
	rows[3].status = statusUnknown
	tbl := renderTable(rows, nil, "", nil, "")
	col := func(name string) int { return len([]rune(tbl.header[:strings.Index(tbl.header, name)])) }
	line := func(i int) []rune { return []rune(strings.Repeat(" ", 4) + tbl.items[i].line) }
	if got := string(line(3)[col("STATUS") : col("STATUS")+7]); got != "unknown" {
		t.Errorf("STATUS cell = %q, want unknown", got)
	}
	if got := string(line(1)[col("RUNNING") : col("RUNNING")+3]); got != "run" {
		t.Errorf("RUNNING cell = %q (column drift after a widened STATUS)", got)
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `cd wt && go test ./internal/tui -run 'TestBuildRowsUnknownLocalStatus|TestBuildRowsLocalModelMissingFromSnapshot|TestRenderTableUnknownStatusAligns' -v`
Expected: FAIL to build — `undefined: statusUnknown`. (`TestBuildRowsMarksAbsentAndRespectsAgentAndFilters` also still passes at this point; it starts failing in Step 3 if the fixture edit from Step 1 were reverted.)

- [ ] **Step 3: Write the minimal implementation**

In `wt/internal/tui/modelrows.go`, add the status value. Order matters for readability only — keep `absent` and `unknown` adjacent so the distinction is visible:

```go
const (
	statusOK      rowStatus = "ok"      // on disk (local) or simply available (cloud)
	statusAbsent  rowStatus = "absent"  // the provider answered and does not have this model
	statusUnknown rowStatus = "unknown" // local model whose presence the probe could not determine
	statusNew     rowStatus = "new"     // discovered, unregistered
)
```

Replace the local branch in `buildRows` (currently the `if loc == config.LocationLocal && in.inventory != nil` block):

```go
		r := tableRow{model: m, location: loc, status: statusOK}
		if loc == config.LocationLocal && in.inventory != nil {
			e, ok := byID[m.ID]
			switch {
			case !ok:
				// No registered entry: the probe skipped this model (an
				// unresolvable location) or its family has no probe at all.
				// Nothing was discovered about it, so presence is unknown.
				r.status = statusUnknown
			case e.Running:
				// Serving right now, so nothing about the row is missing.
				// Checked before the artifact tests because a running
				// mlx_lm_server row never has a resolved artifact.
				r.running = true
			case !e.ArtifactKnown:
				r.status = statusUnknown
			case e.Artifact == "":
				r.status = statusAbsent
			}
		}
```

Update the `tableInput.inventory` doc comment to describe all three states:

```go
// tableInput gathers everything buildRows needs. models is the agent's
// PRE-GATE eligible list (exposed cloud + every configured local model,
// already filtered by agent/-T/-F); inventory is nil when no local probe ran,
// in which case no local row is assessed at all: locals read as not running
// with status ok. When inventory is set, a local row's status is ok, absent
// (the probe answered and found no artifact), unknown (it could not tell), or
// new (discovered, unregistered).
```

`launchable()` needs **no change**: its predicate is already `r.model.ProviderID == "ollama" && r.status != statusAbsent`, so `unknown` fails open, which is the intended fix. Do not add a special case for it.

In `wt/internal/tui/modeltable.go`, make the STATUS column width computed like every other variable-width column. Add `wS` to the declaration:

```go
	famW, idW, costW, w1, w7, w30 := len("FAMILY"), len("MODEL"), len("COST"), len("1D"), len("7D"), len("30D")
	wS := len("STATUS")
```

measure it in the loop (next to the other `maxRunes` calls):

```go
		wS = maxRunes(wS, string(r.status))
```

and use it in both the header and the row:

```go
		padRunes("FAMILY", famW), padRunes("MODEL", idW), padRunes("LOC", 5), padRunes("STATUS", wS),
```
```go
			padRunes(fam[i], famW), padRunes(r.model.ID, idW), padRunes(loc, 5), padRunes(string(r.status), wS),
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `cd wt && go test ./internal/tui -v && gofmt -l .`
Expected: PASS, including the pre-existing `TestRenderTableColumnsAlign` (it derives every offset from the header, so a wider STATUS column keeps its `STATUS`/`RUNNING`/`EXPOSED` slices correct) and `TestLaunchableRules`. `gofmt -l .` prints nothing.

- [ ] **Step 5: Commit**

```bash
git add wt/internal/tui/modelrows.go wt/internal/tui/modeltable.go wt/internal/tui/modelrows_test.go wt/internal/tui/modeltable_test.go
git commit -m "fix(tui): stop reading an unanswered probe as an absent local model - completes plan item #2

A failed ollama probe, and every mlx_lm_server row, used to render STATUS
absent and (for ollama) refuse to launch. Both now read unknown, which
launchable() already fails open on.

Co-Authored-By: Claude Code <noreply@anthropic.com>"
```

---

### Task 3: `wt/CHANGELOG.md` — resolve the committed conflict block

Finding #2. A merge that kept its conflict markers landed in `2391995` and is still present at HEAD, and the Unreleased section still describes the compact one-liner picker this branch replaced (including two bullets about a family-count aggregation that no longer exists). Any later merge of this file treats the markers as conflict content.

**Files:**
- Modify: `wt/CHANGELOG.md` (the `### Changed` bullet at 7-14; the `### Fixed` section at 48-63)

**Interfaces:**
- Consumes: Task 2's `unknown` status (named in the new Fixed bullet).
- Produces: nothing other tasks consume.

- [ ] **Step 1: Confirm the corruption is present**

Run: `git grep -n -e '^<<<<<<< HEAD$' -e '^=======$' -e '^>>>>>>> ' -- wt/CHANGELOG.md`
Expected: three hits (lines 55, 62, 63). If it prints nothing, the markers were already resolved — skip to Step 3.

- [ ] **Step 2: Replace the stale `### Changed` bullet**

Replace the whole first `### Changed` bullet (the `- Model picker rows are now compact one-liners: …` paragraph, lines 7-14) with:

```markdown
- The model picker is now an aligned table with the header rendered as the
  list title: `FAMILY  MODEL  LOC  STATUS  EXPOSED  RUNNING  COST  1D  7D
  30D  SURVEY`. Rows are sorted cost-ascending (output price, then input
  price; local and subscription-only models count as $0, and a model with no
  price data sorts last), then by 7-day usage ascending; non-running local
  models form a second group sorted by id. The previous compact one-liner
  (`family  <fam-30d>  <provider/model>  <location>  <1d/7d/30d> [tags]`) and
  its family divider header rows are gone — including the per-family 30-day
  count column, so there are no inline `[tags]` either. Navigation indices are
  dense (0..n-1); up at the first row wraps to the last and down at the last
  wraps to the first.
```

- [ ] **Step 3: Replace the `### Fixed` section body**

Replace everything between `### Fixed` (line 48) and the `### Removed` heading (line 65) — the family-count bullet, the conflict markers, and both conflicted bullets — with:

```markdown
- The model picker (TUI) fetches the agent's full catalog once and filters it
  in place via `cfg.EligibleModelsIn`, sharing a single traversal with
  `EligibleModels` instead of re-scanning the catalog to build a family-count
  map. (The map and its column are gone with the compact layout.)
- A configured local model now reads `unknown` rather than `absent` when the
  probe could not determine whether its artifact is present — a failed ollama
  probe, or any `mlx_lm_server` row, whose target+draft pairing is not
  discoverable. `absent` is reserved for a provider that answered and does not
  have the model. Previously a transient daemon hiccup made every configured
  ollama model unlaunchable in the TUI even though the same model launched
  fine through `-M` and the non-TUI path.
```

- [ ] **Step 4: Verify the file is clean and the section is coherent**

Run: `git grep -n -e '^<<<<<<< ' -e '^=======$' -e '^>>>>>>> ' -- wt/CHANGELOG.md; grep -c 'fam-30d' wt/CHANGELOG.md; grep -n 'recency-weighted\|AggregateByFamily\|Family 30-day counts' wt/CHANGELOG.md`
Expected: the first grep prints nothing (markers gone). The `grep -c` prints **1** — a single occurrence, inside Step 2's historical parenthetical quoting the format this branch replaced. That mention is intentional and idiomatic for this file: the repo's existing entries name what was removed ("The `d` keybinding in the model picker has been removed", "`-w` short flag … has been removed"), and a changelog reader benefits from seeing what the table replaced. It is not a stale claim. The last grep prints nothing — `recency-weighted` and `AggregateByFamily` describe deleted behavior with no historical framing, so they must be gone.

Do **not** delete the historical `fam-30d` mention to make this step's grep print nothing; an earlier version of this step asked for that and contradicted Step 2, which mandates the text. Correcting the check, not the content, is the resolution.

- [ ] **Step 5: Commit**

```bash
git add wt/CHANGELOG.md
git commit -m "docs(wt): resolve the committed conflict block and correct the stale picker entries - completes plan item #3

Co-Authored-By: Claude Code <noreply@anthropic.com>"
```

---

### Task 4: validate the `-M` pin before probing the inventory

Finding #3, plus finding #5's resolution. `enterModelPhase` probes the local inventory unconditionally and *then* validates the pinned model, so a bad pin pays a full synchronous probe round and throws the snapshot away. The three-line comment above it states its justification twice and gives a reason that is false: it claims the table "is shown again after a cancelled resume prompt", but Esc in `phaseResume` only flips `m.phase` back to `phaseModel` (`app.go:306-310`) — the already-built items persist, so no re-probe is involved.

**Files:**
- Modify: `wt/internal/tui/app.go` (`enterModelPhase`, the comment block at 694-699 and the probe at 700)
- Test: `wt/internal/tui/agent_model_test.go`

**Interfaces:**
- Consumes: the `runInventory` seam (unchanged signature).
- Produces: nothing other tasks consume.

- [ ] **Step 1: Write the failing test**

`wt/internal/tui/agent_model_test.go` does **not** currently import `localmodels` — add it to the import block:

```go
	"github.com/ohanaverse/local-ai-setup/wt/internal/localmodels"
```

Then append, next to the existing `TestPinnedModelInvalidShowsError`:

```go
// TestPinnedModelRejectedSkipsInventoryProbe verifies a rejected -M pin routes
// back to the agent picker without probing the local inventory. The pin is
// judged by localgate's own flag+probe verdict, so the inventory snapshot only
// feeds a table the user never sees — probing first makes a bad pin wait on a
// synchronous network round against every local provider.
func TestPinnedModelRejectedSkipsInventoryProbe(t *testing.T) {
	tempStateDir(t)
	cfg := testConfig()
	probed := false
	old := runInventory
	runInventory = func(*config.Config) localmodels.Snapshot { probed = true; return localmodels.Snapshot{} }
	t.Cleanup(func() { runInventory = old })

	m := model{cfg: cfg, pinnedModel: "ollama/missing", width: 80, height: 24}
	fullCatalog, err := cfg.ModelsForAgent("claude")
	if err != nil {
		t.Fatalf("ModelsForAgent: %v", err)
	}
	models, err := cfg.EligibleModelsIn("claude", fullCatalog, "", "")
	if err != nil {
		t.Fatalf("EligibleModelsIn: %v", err)
	}
	if _, cmd := m.enterModelPhase("claude", models, "code"); cmd != nil {
		t.Errorf("expected nil cmd (pin rejected), got %v", cmd)
	}
	if probed {
		t.Error("inventory was probed for a rejected pin whose table is never rendered")
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `cd wt && go test ./internal/tui -run TestPinnedModelRejectedSkipsInventoryProbe -v`
Expected: FAIL with `inventory was probed for a rejected pin whose table is never rendered`.

- [ ] **Step 3: Reorder and re-comment**

In `enterModelPhase`, replace the comment block and the probe line (currently lines 694-701, i.e. the comment through `inv := runInventory(m.cfg)` / `snap := &inv`) with a pin-validation block that runs **before** the probe:

```go
	// A -M pin keeps localgate's flag+probe verdict. Validate it BEFORE probing
	// the inventory: a rejected pin routes back to the agent picker and never
	// renders a table, so probing first would make the user wait on a
	// synchronous round-trip against every local provider for nothing.
	//
	// The inventory is then probed once, for the table's live STATUS/RUNNING
	// columns. It is deliberately a separate probe from localgate.Apply's: Apply
	// answers "may this local model launch?", and it does so by name-checking
	// each provider's /v1/models, while the table needs the full registered +
	// discovered artifact inventory. Neither result can be derived from the
	// other, so this is one probe per question, not a duplicated one.
	if m.pinnedModel != "" {
		gate := localgate.Apply(m.cfg, models, m.pinnedModel)
		if gate.PinnedRejected != nil {
			return routeBack(gate.PinnedRejected.Error())
		}
		if config.IndexModelByID(gate.Eligible, m.pinnedModel) < 0 {
			return routeBack(fmt.Sprintf("model %q is not in the eligible list for agent %q", m.pinnedModel, agent))
		}
		models = gate.Eligible
	}

	inv := runInventory(m.cfg)
	snap := &inv
```

While editing this function's comments, record finding #5's conclusion so the next reader does not "fix" it — insert this paragraph into the `enterModelPhase` doc comment **immediately before the final sentence** (`// Otherwise the only route-back is an empty table.`), so it reads as one more property of the pre-gate list rather than trailing after that sentence:

```go
// The header's tag line and hideDiscovered answer two different questions and
// are intentionally not the same value: the tag line names the ROTATION group
// (firstTag, defaulting to DefaultTag), which is what the cursor positions
// against, while hideDiscovered tracks an explicit -T/-F narrowing, because
// discovered models are unregistered and cannot be filtered by tag or family.
// With a DefaultTag set and no -T, the list is therefore unfiltered by tag
// (EligibleModelsIn only filters when a tag set is present) while the header
// still names the rotation group.
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `cd wt && go test ./internal/tui -v && gofmt -l .`
Expected: PASS, including the pre-existing `TestPinnedModelInvalidShowsError`, `TestPinnedModelValidSkipsPicker`, and `TestPinnedModelWithoutAgentValidatesAfterAgentPick`.

- [ ] **Step 5: Commit**

```bash
git add wt/internal/tui/app.go wt/internal/tui/agent_model_test.go
git commit -m "perf(tui): validate the -M pin before probing the local inventory - completes plan item #4

Also replaces a duplicated comment that justified the old order with a
reason that was false (a cancelled resume prompt never rebuilds the table).

Co-Authored-By: Claude Code <noreply@anthropic.com>"
```

---

### Task 5: delete the unreachable empty-table branch

Finding #7. `len(tbl.items) == 0` cannot hold: `buildRows` appends exactly one row per element of `models` (the append is unconditional), and both callers guard `len(models) == 0` with their own status message first (`app.go:359-362` for the agent picker, `646-647` for the pinned-agent path). The branch's message is also the generic `no models for agent %q — edit your config`, which replaced the actionable `modelman start` wording of the gate-emptied case it superseded.

**Files:**
- Modify: `wt/internal/tui/app.go` (`enterModelPhase`: the `routeBack` call at 724-726 and the doc comment at 665-679)

**Interfaces:**
- Consumes: nothing.
- Produces: nothing.

- [ ] **Step 1: Confirm the branch is unreachable**

Run: `cd wt && grep -n "enterModelPhase" internal/tui/*.go | grep -v _test`
Expected: exactly two call sites (`app.go:363`, `app.go:648`), each immediately preceded by a `len(models) == 0` guard. Also confirm `buildRows` appends unconditionally: `grep -n "rows = append(rows, r)" internal/tui/modelrows.go` → one hit, outside any conditional.

- [ ] **Step 2: Delete the branch**

Remove these lines from `enterModelPhase`:

```go
	if len(tbl.items) == 0 {
		return routeBack(fmt.Sprintf("no models for agent %q — edit your config", agent))
	}
```

`routeBack` stays — the two pin-rejection paths still use it. Leave `buildTable`'s other arguments untouched.

- [ ] **Step 3: Document the precondition**

Add to the `enterModelPhase` doc comment (which currently ends with "Otherwise the only route-back is an empty table.") — replace that sentence with:

```go
// models must be non-empty: buildRows emits one row per model, so an empty
// table is only possible from an empty input, and both callers (the phaseAgent
// Enter path and proceedFromSelectedPath's pinned-agent path) already guard
// len(models) == 0 with their own status message. A future caller must do the
// same — there is no empty-table branch here to catch it.
//
// The only route-back is a rejected -M pin.
```

- [ ] **Step 4: Verify the suite still passes**

This is a pure deletion, so there is no new test: the deliverable is that the picker behaves identically and the compiler confirms `fmt`/`routeBack` are still used.

Run: `cd wt && go build ./... && go vet ./... && go test ./internal/tui -v`
Expected: PASS, no `declared and not used` errors. (`fmt` is still used elsewhere in `app.go`; if the build reports it unused, the branch was not the only remaining user — stop and re-check before removing the import.)

- [ ] **Step 5: Commit**

```bash
git add wt/internal/tui/app.go
git commit -m "refactor(tui): drop the unreachable empty-table branch - completes plan item #5

buildRows emits one row per model and both callers guard an empty model
list, so this branch could never fire; its generic message also lost the
actionable modelman-start wording of the case it superseded.

Co-Authored-By: Claude Code <noreply@anthropic.com>"
```

---

### Task 6: drop the dead `modelItem.row` field

Finding #8. `modelItem.row` is written in `renderTable` and read nowhere. Its doc comment claims it serves "rendered cells and header alignment", which the `line` field already does — so it misleads a reader into thinking it is load-bearing.

**Files:**
- Modify: `wt/internal/tui/model_list.go` (the `modelItem` struct and its doc comment at 41-51)
- Modify: `wt/internal/tui/modeltable.go` (the composite literal at 112)

**Interfaces:**
- Consumes: nothing.
- Produces: nothing.

- [ ] **Step 1: Confirm nothing reads it**

Run: `cd wt && grep -rn "\.row\b\|row:" --include='*.go' internal/tui/`
Expected: the write at `modeltable.go:112` and the struct field, plus `modelrows_test.go:165` — which is a test-case struct's own `row` field on an unrelated anonymous struct, not `modelItem.row`. No read of `modelItem.row`.

- [ ] **Step 2: Delete the field, its doc sentence, and the initializer**

In `model_list.go`, drop the field and the sentence describing it from the struct doc comment:

```go
type modelItem struct {
	model     config.Model
	line      string
	marked    bool
	ref       int
	exception string
	blocked   string // non-empty: Enter shows this instead of launching
}
```

In `modeltable.go`, drop the initializer:

```go
		it := &modelItem{model: r.model, line: line, marked: lastID != "" && r.model.ID == lastID, ref: refs[r.model.ID]}
```

- [ ] **Step 3: Verify the suite still passes**

Run: `cd wt && go build ./... && go vet ./... && go test ./internal/tui -v`
Expected: PASS. Deleting a written-but-never-read field changes no behavior, so no test asserts it.

- [ ] **Step 4: Commit**

```bash
git add wt/internal/tui/model_list.go wt/internal/tui/modeltable.go
git commit -m "refactor(tui): remove the dead modelItem.row field - completes plan item #6

Co-Authored-By: Claude Code <noreply@anthropic.com>"
```

---

### Task 7: make the seam convention true for both offenders

Finding #9. `wt/CLAUDE.md`'s "Test seams" section states the convention: *"a `var x = realX` plus a `realX` function."* Two seams violate it — this branch's `runInventory`, and the pre-existing `installed = agents.Installed`. Fixing only the new one leaves the documented invariant half-true, which is how a convention erodes.

**Files:**
- Modify: `wt/internal/tui/app.go` (the `runInventory` seam at 662-663)
- Modify: `wt/internal/tui/agent_picker.go` (the `installed` seam at 11-14)

**Interfaces:**
- Consumes: `localmodels.Inventory(cfg *config.Config) localmodels.Snapshot` and `agents.Installed(bin string) bool` (both unchanged).
- Produces: `realInventory`, `realInstalled` — package-private production wrappers.

- [ ] **Step 1: Confirm the violations**

Run: `cd wt && grep -rn "^var [a-zA-Z]* = \|^func real[A-Z]" --include='*.go' internal/tui/`
Expected: `emitPriceNotice`/`newSurveyStore`/`runSurvey`/`newUsageStore`/`newRefcountStore` all have `real*` partners; `installed` and `runInventory` do not.

- [ ] **Step 2: Add the wrappers**

In `app.go`:

```go
// runInventory is a test seam: production uses realInventory.
var runInventory = realInventory

// realInventory is the production implementation of the runInventory seam: a
// live probe of every local provider.
func realInventory(cfg *config.Config) localmodels.Snapshot { return localmodels.Inventory(cfg) }
```

In `agent_picker.go`:

```go
// installed is a test seam: production uses realInstalled. Tests override it to
// control the installed state deterministically without depending on the host's
// installed binaries.
var installed = realInstalled

// realInstalled is the production implementation of the installed seam: a real
// PATH lookup.
func realInstalled(bin string) bool { return agents.Installed(bin) }
```

Keep the existing comment text above each var if it says more; the point is that the var's value is now a named production function rather than a foreign package's function value.

- [ ] **Step 3: Verify the suite still passes**

Run: `cd wt && go build ./... && go vet ./... && go test ./internal/tui -v`
Expected: PASS. Existing tests assign to the *var* (`TestMain`, `stubInventory`, the `installed = …` overrides), which is unaffected by what the var's default value names.

- [ ] **Step 4: Commit**

```bash
git add wt/internal/tui/app.go wt/internal/tui/agent_picker.go
git commit -m "refactor(tui): give runInventory and installed real* wrappers per the seam convention - completes plan item #7

Co-Authored-By: Claude Code <noreply@anthropic.com>"
```

---

### Task 8: sync the living docs

Finding #4 (the user guide still documents the compact one-liner and its family-usage sort, which this branch removed) plus the doc debt Task 1 and Task 2 create: a new STATUS value and a new `Entry` field that a reader of `wt/CLAUDE.md` cannot discover.

**Files:**
- Modify: `docs/guides/06-wt-agents-and-models.md` (the picker row-format sentence at line 68)
- Modify: `wt/CLAUDE.md` (the selector-table sentence in the `internal/tui` package row; the TUI model picker paragraph in "Local-model gate"; the `internal/localmodels` package row)

**Interfaces:**
- Consumes: Task 2's `unknown` status, Task 1's `ArtifactKnown`, Task 6's field removal (all already committed).
- Produces: nothing.

- [ ] **Step 1: Rewrite the guide's picker description**

In `docs/guides/06-wt-agents-and-models.md`, replace the row-format and sort sentence (line 68, beginning "The tag slot defaults to" and running through "…(see §5).") with:

```markdown
  The tag slot names the **rotation** group: it defaults to `default_tag = "code"` (see §6), and `-T` narrows it. Models are one table row each, with the header rendered as the list title: `FAMILY  MODEL  LOC  STATUS  EXPOSED  RUNNING  COST  1D  7D  30D  SURVEY`. Rows are sorted cost-ascending (output price, then input price; local and subscription-only models count as $0, and a model with no price data sorts last within that group), then by 7-day usage ascending, then by id; non-running local models form a second group, sorted by id. STATUS is `ok`, `absent` (the provider answered and does not have the model), `unknown` (the probe could not tell — a failed local probe, or a non-running `mlx_lm_server` row), or `new` (discovered, not in the registry); RUNNING is `run` while a model is serving. Note that the list is **not** filtered by `default_tag` — only an explicit `-T`/`-F` narrows it, and only those hide discovered (unregistered) rows. After `/`, typing a family name (or any part of a model ID) narrows the list. The cursor still starts on the rotation's next model (see §5).
```

- [ ] **Step 2: Update `wt/CLAUDE.md`**

In the `internal/tui` package row, insert this immediately after the text `for header and aligned rendering.` and **inside the same table cell**: the row does not end there — a `runInventory` sentence follows it, and the cell closes with ` |`, so your text must land before that pipe. Appending after the row would break the markdown table:

```markdown
`buildRows` assigns STATUS from `localmodels.Entry.ArtifactKnown`: `absent` only when the probe answered and found no artifact, `unknown` when it could not tell (an unreachable family, a model with no inventory entry, or any non-running `mlx_lm_server` row) — and a row that is `Running` is never absent and never `unknown`. `launchable()` fails open on `unknown` for ollama, matching the non-TUI path's unconditional trust of an ollama flag.
```

In the "Local-model gate (multi-model design, 2026-09-14)" section's **TUI model picker** paragraph, the sentence "Launch is gated via `tableRow.launchable()` (true for running models, or a pulled ollama model since ollama loads on demand)" becomes:

```markdown
Launch is gated via `tableRow.launchable()` (true for running models, or an ollama model the probe did not find *absent* since ollama loads on demand — so an `unknown` row stays launchable and only a probed-absent one is refused).
```

In the `internal/localmodels` package row, insert this immediately after the text `(`FamilyOrigin`).` and before the cell's closing ` |` — again inside the same cell, so the markdown table does not break:

```markdown
`Entry.ArtifactKnown` distinguishes an artifact the probe confirmed missing from one it could not determine (an unreachable family; `mlx_lm_server`, which has no artifact discovery).
```

- [ ] **Step 3: Verify the docs**

Run: `cd .. && make check-links` (from the worktree root — the link checker validates repo-relative markdown links across `docs/guides`, `docs/reference`, and the READMEs), then `git grep -n 'fam-30d\|recency-weighted composite' docs/ wt/CLAUDE.md`
Expected: `make check-links` passes; the grep prints nothing.

- [ ] **Step 4: Commit**

```bash
git add docs/guides/06-wt-agents-and-models.md wt/CLAUDE.md
git commit -m "docs(wt): document the selector table's real status values and drop the stale picker format - completes plan item #8

Co-Authored-By: Claude Code <noreply@anthropic.com>"
```

---

### Task 9: full local gate

Nothing here changes behavior; it is the verification that the eight commits above compose — `internal/localmodels` and `internal/tui` are the only packages touched, but `cmd/wt` consumes both.

**Files:** none.

- [ ] **Step 1: Run the whole Go gate from the module root**

Run: `cd wt && go build ./... && go vet ./... && go test ./... && gofmt -l .`
Expected: build and vet clean; all tests pass; `gofmt -l .` prints nothing.

- [ ] **Step 2: Run the repo-wide aggregate**

Run: `cd .. && make test-all`
Expected: PASS — root lint + `check-links`, modelman's `make check`/`make test`, and wt's `go build`/`vet`/`test`. If modelman's suite is unaffected (it should be — no Python files are touched), a failure there indicates a pre-existing issue, not a regression from this plan; report it rather than "fixing" it here.

- [ ] **Step 3: Report, and stop short of a PR**

Summarize the nine findings and their resolution, then **ask** whether to push the branch or open a PR. Do not run `gh pr create` or `git push` without explicit approval.

---

## Notes on the findings this plan does not change

Recorded here so the review's record is complete and the next reader does not re-open them.

- **Finding #5 (hideDiscovered ignores the effective tag group) — premise refuted.** `config.EligibleModelsIn` filters by tag only when `len(tagSet) > 0`; it has **no `DefaultTag` fallback**, so `default_tag = "code"` does not hide registered non-code models and the review's failure scenario cannot occur. The tag line in the header names the *rotation* group (`firstTag`), which is what `rotation.Next` positions the cursor against, while `hideDiscovered` tracks an explicit `-T`/`-F` narrowing — two different questions. Task 4's comment records the distinction in the code; Task 8's guide edit records it for users. The residual oddity (with `default_tag="code"` and no `-T`, the header reads `tag : code` while discovered rows show, whereas `-T code` names the same group and hides them) is inherent to `-T` being both a filter and a rotation-slot override; making `hideDiscovered` follow `DefaultTag` would contradict `wt/CLAUDE.md`'s documented "lists all configured and discovered local models" contract for this picker, so it was rejected.

- **Finding #6 (the `wt smoke` picker pays a second probe round) — intentional, accepted.** `wt/CLAUDE.md` documents it: the picker probes `localmodels.Inventory` so its STATUS/RUNNING columns show current local-model state, and `smoke.Eligibility`'s `localgate.ResolveAll` does not return per-entry `Artifact`/`Running`, so there is nothing to reuse without changing `Eligibility`'s signature. Left as-is; the duplicate `/v1/models` latency is a known cost.

- **Pre-existing, flagged but not fixed: `wt/CHANGELOG.md` has two `### Changed` headings under `Unreleased`** (lines 5 and 70). That is wrong for a single release section, but it predates this branch and merging the sections would move unrelated bullets, making the Task 3 diff harder to review. Not part of any finding.

- **`installed`'s missing wrapper was a second instance of finding #9**, not covered by the review — fixed in Task 7 so the documented convention holds for every seam.
