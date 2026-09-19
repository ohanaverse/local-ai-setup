# wt: modelman-style model selector table — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace wt's model picker rows with a modelman-style table (column headings; FAMILY/MODEL/LOC/STATUS/EXPOSED/RUNNING/COST/1D/7D/30D/SURVEY) that lists exposed cloud models, configured local models and discovered local models, with the two-group sort from the spec.

**Architecture:** A pure rows layer (`buildRows` / `sortRows` / `launchable`) feeds a renderer that produces a header line plus `modelItem`s; the Bubbles `list` is kept (its `Title` carries the header). `enterModelPhase` builds rows from the pre-gate eligible models plus a `localmodels.Inventory` snapshot (through a `runInventory` test seam). `wt smoke`'s `PickModel` uses the same builder. The old family-usage builder and its tests are retired.

**Tech Stack:** Go 1.26 (module root `wt/`), Bubble Tea + Bubbles `list`, lipgloss, stdlib `testing`.

**Spec:** `docs/superpowers/specs/2026-09-19-wt-model-selector-table-design.md`

## Global Constraints

- Columns and order: `FAMILY  MODEL  LOC  STATUS  EXPOSED  RUNNING  COST  1D  7D  30D  SURVEY`, cells separated by two spaces; MODEL shows the id (`provider/model`); the rotation marker and in-use ref count stay a 4-character left prefix (composed by `modelItem.Title()`); mid-row cells are plain ASCII.
- Cell values: LOC `cloud`/`local`; STATUS `ok`/`absent`/`new`; EXPOSED `Y`/`-`; RUNNING `run`/`-`; discovered rows: family `-`, exposed `-`, cost `-`.
- Row sources: exposed cloud models (existing predicate) + every configured local model + discovered local models, all limited to providers the chosen agent supports (`supported_providers` stays a hard constraint). Discovered rows are hidden when `-T`/`-F` is set. A discovered row whose id equals an existing row id is dropped (registry row wins).
- Sort: group 1 = cloud rows + running local rows, by cost ascending (output price per million, then input price per million; local and subscription-only rows count as $0; a row with no cost data at all sorts last within the group), then 7-day usage ascending, then id. Group 2 = non-running local rows, alphabetical by id. No divider row.
- A running row launches through the existing path; a non-running local row does not launch (status hint) EXCEPT a pulled ollama model, which stays launchable. With LiteLLM routing on or a forced route, a discovered row shows `(not in LiteLLM)` and cannot be selected.
- Counts: `usage.CountsForAgent` for the chosen agent-model pair; model-level `Counts` when there is no agent (`wt smoke`). Survey segment stays a trailing `survey.FormatPickerSegment` per pair.
- The `Inventory` probe is synchronous inside `enterModelPhase` (no new phase). The non-TUI path and the `-M` pin check keep using `localgate` unchanged.
- The trailing `[tags]` cell is dropped.
- Every `Test*` needs a top-level `//` comment stating what it tests and why it matters (repo rule, `wt/CLAUDE.md`).
- Run Go commands from `wt/`. Run `go vet ./...` and `gofmt -l .` before each commit (wt-ci gates on gofmt).
- Commit messages during execution end with `- completes plan item #N` and a `Co-Authored-By:` trailer naming the model that authored the commit.
- Execute in an isolated worktree (superpowers:using-git-worktrees), never with `main` checked out in a linked worktree. Do not push or open a PR without asking.

## File Structure

| File | Responsibility |
|---|---|
| `wt/internal/config/config.go` | Modify: `ExposedFlag`, `AgentSupportsProvider` |
| `wt/internal/usage/usage.go` | Modify: `Store` interface gains `CountsForAgent` |
| `wt/internal/tui/model_line_test.go` | Modify: `mockStore.CountsForAgent` |
| `wt/internal/tui/modelrows.go` | Create: `tableRow`, `tableInput`, `buildRows`, `sortRows`, `launchable`, `notLaunchableHint` |
| `wt/internal/tui/modeltable.go` | Create: column widths, header + line rendering, `renderTable`, `buildTable` |
| `wt/internal/tui/model_list.go` | Modify: `modelItem` fields; later remove legacy builder |
| `wt/internal/tui/app.go` | Modify: `enterModelPhase`, phaseModel Enter guard, list header |
| `wt/internal/tui/pick_model.go` | Modify: use `buildTable` |
| `wt/internal/tui/testhelpers_test.go` | Modify: `TestMain` stub, `tableItems` helper |
| existing tui tests | Modify/delete per Task 4/5 dispositions |
| `wt/CLAUDE.md` | Modify: docs |

---

### Task 1: Config accessors and `usage.Store.CountsForAgent`

**Files:**
- Modify: `wt/internal/config/config.go` (next to `IsExposed`, ~line 578), `wt/internal/usage/usage.go` (`Store` interface), `wt/internal/tui/model_line_test.go` (`mockStore`)
- Test: `wt/internal/config/config_test.go`, `wt/internal/usage/usage_test.go`

**Interfaces:**
- Consumes: `Config.exposed` map (`ExposureEntry{Exposed,Ready}`), `Config.AgentByName`, `usage.StoreImpl.CountsForAgent` (already exists, #110).
- Produces:
  - `func (c *Config) ExposedFlag(id string) bool` — modelman's raw `exposed` flag; unlike `IsExposed` it never treats local models as always exposed.
  - `func (c *Config) AgentSupportsProvider(agentName, providerID string) bool` — false for an unknown agent.
  - `usage.Store` gains `CountsForAgent(agent string, modelIDs []string) map[string]UsageCounts`.

- [ ] **Step 1: Write the failing tests**

`config_test.go`:
```go
// TestExposedFlagIsRawModelmanFlag verifies ExposedFlag reports modelman.toml's
// raw exposed flag — false for a local model with no flag even though
// IsExposed treats every local model as exposed. The selector's EXPOSED
// column must mirror modelman, not wt's catalog-membership predicate.
func TestExposedFlagIsRawModelmanFlag(t *testing.T) {
	cfg := &Config{
		Providers: []Provider{{ID: "omlx", Location: LocationLocal}},
		Models:    []Model{{ID: "omlx/a", ProviderID: "omlx"}, {ID: "omlx/b", ProviderID: "omlx"}},
	}
	cfg.SetExposedForTest(map[string]ExposureEntry{"omlx/a": {Exposed: true}})
	if !cfg.ExposedFlag("omlx/a") {
		t.Error("omlx/a: want exposed")
	}
	if cfg.ExposedFlag("omlx/b") || cfg.ExposedFlag("missing") {
		t.Error("unflagged/missing ids must not read as exposed")
	}
	if !cfg.IsExposed(cfg.Models[1]) {
		t.Fatal("precondition: IsExposed treats local models as exposed")
	}
}

// TestAgentSupportsProvider verifies the agent's supported_providers list is
// the check, and an unknown agent supports nothing — the selector uses it to
// decide which discovered models an agent may show.
func TestAgentSupportsProvider(t *testing.T) {
	cfg := &Config{Agents: []Agent{{Name: "claude", SupportedProviders: []string{"ollama", "omlx"}}}}
	if !cfg.AgentSupportsProvider("claude", "omlx") {
		t.Error("claude should support omlx")
	}
	if cfg.AgentSupportsProvider("claude", "mtplx") || cfg.AgentSupportsProvider("nope", "omlx") {
		t.Error("unsupported provider / unknown agent must be false")
	}
}
```
`usage_test.go`:
```go
// TestStoreInterfaceIncludesCountsForAgent is a compile-time guard that the
// Store interface exposes per-agent counts, so the selector can take a Store
// (and tests a mock) instead of the concrete StoreImpl.
func TestStoreInterfaceIncludesCountsForAgent(t *testing.T) {
	var s Store = NewStoreAt(t.TempDir())
	if got := s.CountsForAgent("claude", []string{"m"}); got["m"] != (UsageCounts{}) {
		t.Errorf("got %+v, want zero", got["m"])
	}
}
```

- [ ] **Step 2: Run to verify failure**

Run: `go test ./internal/config ./internal/usage -run 'ExposedFlag|AgentSupportsProvider|StoreInterfaceIncludesCountsForAgent' -v`
Expected: FAIL to compile (`ExposedFlag`, `AgentSupportsProvider` undefined; `Store` lacks `CountsForAgent`).

- [ ] **Step 3: Implement**

`config.go`:
```go
// ExposedFlag reports modelman's raw `exposed` flag for the model id (legacy
// litellm_exposed ORed in at load). Unlike IsExposed it never treats local
// models as always exposed, so it is what a table mirroring modelman's EXPOSED
// column should read.
func (c *Config) ExposedFlag(id string) bool {
	st, ok := c.exposed[id]
	return ok && st.Exposed
}

// AgentSupportsProvider reports whether the named agent lists providerID in
// supported_providers. An unknown agent supports nothing.
func (c *Config) AgentSupportsProvider(agentName, providerID string) bool {
	a, err := c.AgentByName(agentName)
	if err != nil {
		return false
	}
	for _, pid := range a.SupportedProviders {
		if pid == providerID {
			return true
		}
	}
	return false
}
```
`usage.go`: add `CountsForAgent(agent string, modelIDs []string) map[string]UsageCounts` to the `Store` interface.
`model_line_test.go`: add
```go
func (s *mockStore) CountsForAgent(agent string, ids []string) map[string]usage.UsageCounts {
	return s.Counts(ids)
}
```

- [ ] **Step 4: Run to verify pass**

Run: `go build ./... && go vet ./... && go test ./...` — Expected: PASS (fix any other `usage.Store` implementer the compiler reports).

- [ ] **Step 5: Commit**

```bash
gofmt -l internal
git add wt/internal/config wt/internal/usage wt/internal/tui/model_line_test.go
git commit -m "feat(config): ExposedFlag, AgentSupportsProvider; usage.Store.CountsForAgent - completes plan item #1"
```

---

### Task 2: Rows layer — build, sort, launchable

**Files:**
- Create: `wt/internal/tui/modelrows.go`, `wt/internal/tui/modelrows_test.go`

**Interfaces:**
- Consumes: Task 1 (`ExposedFlag`, `AgentSupportsProvider`, `Store.CountsForAgent`), `localmodels.Snapshot`/`Entry` (#111), `config.ResolveLocation`, `survey.Stats`, `localgate.NotRunningError`.
- Produces:
  - `type rowStatus string` with `statusOK="ok"`, `statusAbsent="absent"`, `statusNew="new"`
  - `type tableRow struct { model config.Model; location config.Location; status rowStatus; exposed, running, discovered bool; counts usage.UsageCounts; stats survey.Stats }`
  - `type tableInput struct { cfg *config.Config; agent string; models []config.Model; inventory *localmodels.Snapshot; hideDiscovered bool; usage usage.Store; stats map[string]survey.Stats }`
  - `func buildRows(in tableInput) []tableRow`
  - `func sortRows(rows []tableRow)`
  - `func (r tableRow) launchable() bool`
  - `func (r tableRow) notLaunchableHint() string`

- [ ] **Step 1: Write the failing tests** (`modelrows_test.go`)

```go
package tui

import (
	"strings"
	"testing"

	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/localmodels"
	"github.com/ohanaverse/local-ai-setup/wt/internal/usage"
)

func f64(v float64) *float64 { return &v }

func rowsTestCfg() *config.Config {
	cfg := &config.Config{
		Providers: []config.Provider{
			{ID: "openrouter", Location: config.LocationCloud},
			{ID: "omlx", Location: config.LocationLocal},
			{ID: "ollama", Location: config.LocationLocal},
			{ID: "mtplx", Location: config.LocationLocal},
		},
		Agents: []config.Agent{{Name: "claude", SupportedProviders: []string{"openrouter", "omlx", "ollama"}}},
	}
	cfg.SetExposedForTest(map[string]config.ExposureEntry{"openrouter/cheap": {Exposed: true, Ready: true}})
	return cfg
}

func rowIDs(rows []tableRow) []string {
	ids := make([]string, len(rows))
	for i, r := range rows {
		ids[i] = r.model.ID
	}
	return ids
}

// TestBuildRowsIncludesConfiguredAndDiscoveredLocal verifies the three row
// sources: a configured cloud model, a configured local model (running or
// not), and a discovered on-disk model with no registry entry — the union the
// selector is meant to show.
func TestBuildRowsIncludesConfiguredAndDiscoveredLocal(t *testing.T) {
	cfg := rowsTestCfg()
	models := []config.Model{
		{ID: "openrouter/cheap", ProviderID: "openrouter", ModelName: "cheap"},
		{ID: "omlx/a", ProviderID: "omlx", ModelName: "a", Family: "fam"},
	}
	inv := &localmodels.Snapshot{Entries: []localmodels.Entry{
		{ProviderID: "omlx", ModelID: "omlx/a", Artifact: "a", Registered: true, Running: true},
		{ProviderID: "omlx", ModelID: "omlx/disc", Artifact: "disc"},
	}}
	rows := buildRows(tableInput{cfg: cfg, agent: "claude", models: models, inventory: inv, usage: usage.NewStoreAt(t.TempDir())})

	if got := strings.Join(rowIDs(rows), ","); got != "openrouter/cheap,omlx/a,omlx/disc" {
		t.Fatalf("rows = %s", got)
	}
	if !rows[0].exposed || rows[0].location != config.LocationCloud || rows[0].status != statusOK {
		t.Errorf("cloud row = %+v", rows[0])
	}
	if !rows[1].running || rows[1].status != statusOK || rows[1].discovered {
		t.Errorf("configured local row = %+v", rows[1])
	}
	d := rows[2]
	if !d.discovered || d.status != statusNew || d.exposed || d.running || d.model.ModelName != "disc" || d.location != config.LocationLocal {
		t.Errorf("discovered row = %+v", d)
	}
}

// TestBuildRowsMarksAbsentAndRespectsAgentAndFilters verifies: a configured
// local model with no artifact reads "absent"; discovered models from a
// provider the agent does not support are skipped (agent supported_providers
// stays a hard constraint); hideDiscovered (-T/-F) drops discovered rows; a
// discovered id equal to an existing row id is dropped (registry row wins).
func TestBuildRowsMarksAbsentAndRespectsAgentAndFilters(t *testing.T) {
	cfg := rowsTestCfg()
	models := []config.Model{{ID: "omlx/gone", ProviderID: "omlx", ModelName: "gone"}}
	inv := &localmodels.Snapshot{Entries: []localmodels.Entry{
		{ProviderID: "omlx", ModelID: "omlx/gone", Registered: true},
		{ProviderID: "mtplx", ModelID: "mtplx/x", Artifact: "x"},        // claude does not support mtplx
		{ProviderID: "ollama", ModelID: "omlx/gone", Artifact: "dup"},   // id collision with a row
		{ProviderID: "ollama", ModelID: "ollama/ok", Artifact: "ok"},
	}}
	in := tableInput{cfg: cfg, agent: "claude", models: models, inventory: inv, usage: usage.NewStoreAt(t.TempDir())}
	rows := buildRows(in)
	if got := strings.Join(rowIDs(rows), ","); got != "omlx/gone,ollama/ok" {
		t.Fatalf("rows = %s", got)
	}
	if rows[0].status != statusAbsent {
		t.Errorf("status = %q, want absent", rows[0].status)
	}
	in.hideDiscovered = true
	if got := strings.Join(rowIDs(buildRows(in)), ","); got != "omlx/gone" {
		t.Errorf("hideDiscovered rows = %s", got)
	}
}

// TestBuildRowsCountsAreAgentScoped verifies the 1d/7d/30d counts come from
// CountsForAgent when an agent is chosen (only that pair's launches) and from
// the model-level Counts when there is no agent (wt smoke).
func TestBuildRowsCountsAreAgentScoped(t *testing.T) {
	cfg := rowsTestCfg()
	store := usage.NewStoreAt(t.TempDir())
	_ = store.RecordFor("claude", "openrouter/cheap")
	_ = store.RecordFor("codex", "openrouter/cheap")
	models := []config.Model{{ID: "openrouter/cheap", ProviderID: "openrouter"}}

	byAgent := buildRows(tableInput{cfg: cfg, agent: "claude", models: models, usage: store})
	if byAgent[0].counts.ThirtyDay != 1 {
		t.Errorf("claude pair 30d = %d, want 1", byAgent[0].counts.ThirtyDay)
	}
	noAgent := buildRows(tableInput{cfg: cfg, models: models, usage: store})
	if noAgent[0].counts.ThirtyDay != 2 {
		t.Errorf("model-level 30d = %d, want 2", noAgent[0].counts.ThirtyDay)
	}
}

// TestSortRowsTwoGroups verifies the spec's sort: group 1 (cloud + running
// local) by cost ascending — output price, then input price; local and
// subscription-only count as $0; no cost data last — then 7-day usage
// ascending; group 2 (non-running local) alphabetical. Cheap and already-
// running models must come first.
func TestSortRowsTwoGroups(t *testing.T) {
	cloud := func(id string, in, out *float64, sub *float64) tableRow {
		return tableRow{location: config.LocationCloud, model: config.Model{ID: id, Cost: config.ModelCost{InputPricePerMillion: in, OutputPricePerMillion: out, SubscriptionPrice: sub}}}
	}
	local := func(id string, running bool, sevenDay int) tableRow {
		return tableRow{location: config.LocationLocal, running: running, model: config.Model{ID: id}, counts: usage.UsageCounts{SevenDay: sevenDay}}
	}
	rows := []tableRow{
		local("z-off", false, 0),
		cloud("pricey", f64(3), f64(15), nil),
		cloud("nodata", nil, nil, nil),
		local("run-busy", true, 5),
		cloud("sub", nil, nil, f64(100)),
		cloud("cheap-in-hi", f64(2), f64(1), nil),
		cloud("cheap-in-lo", f64(1), f64(1), nil),
		local("a-off", false, 0),
		local("run-idle", true, 1),
	}
	sortRows(rows)
	// $0 rows (sub, run-idle, run-busy) tie on cost and order by 7d usage
	// ascending: sub(0), run-idle(1), run-busy(5). Then priced cloud rows by
	// output then input price, then the no-data row; group 2 alphabetical.
	want := "sub,run-idle,run-busy,cheap-in-lo,cheap-in-hi,pricey,nodata,a-off,z-off"
	if got := strings.Join(rowIDs(rows), ","); got != want {
		t.Errorf("order = %s\nwant    %s", got, want)
	}
}

// TestLaunchableRules verifies which rows Enter may launch: cloud always; a
// running local row; a pulled ollama model even when not loaded (ollama loads
// on demand) — but not an absent ollama model, and not a non-running omlx row.
func TestLaunchableRules(t *testing.T) {
	cases := []struct {
		name string
		row  tableRow
		want bool
	}{
		{"cloud", tableRow{location: config.LocationCloud}, true},
		{"running local", tableRow{location: config.LocationLocal, running: true, model: config.Model{ProviderID: "omlx"}}, true},
		{"idle ollama pulled", tableRow{location: config.LocationLocal, status: statusOK, model: config.Model{ProviderID: "ollama"}}, true},
		{"idle ollama discovered", tableRow{location: config.LocationLocal, status: statusNew, model: config.Model{ProviderID: "ollama"}}, true},
		{"absent ollama", tableRow{location: config.LocationLocal, status: statusAbsent, model: config.Model{ProviderID: "ollama"}}, false},
		{"idle omlx", tableRow{location: config.LocationLocal, status: statusOK, model: config.Model{ProviderID: "omlx"}}, false},
	}
	for _, tc := range cases {
		if got := tc.row.launchable(); got != tc.want {
			t.Errorf("%s: launchable = %v, want %v", tc.name, got, tc.want)
		}
	}
}

// TestNotLaunchableHint verifies the status text names the fix: `modelman
// start <id>` for a configured model, the provider CLI for a discovered one.
func TestNotLaunchableHint(t *testing.T) {
	reg := tableRow{model: config.Model{ID: "omlx/a", ProviderID: "omlx"}}
	if h := reg.notLaunchableHint(); !strings.Contains(h, "modelman start omlx/a") {
		t.Errorf("registered hint = %q", h)
	}
	disc := tableRow{discovered: true, model: config.Model{ID: "omlx/d", ProviderID: "omlx"}}
	if h := disc.notLaunchableHint(); !strings.Contains(h, "omlx/d") || strings.Contains(h, "modelman start") {
		t.Errorf("discovered hint = %q", h)
	}
}
```

- [ ] **Step 2: Run to verify failure**

Run: `go test ./internal/tui -run 'BuildRows|SortRowsTwoGroups|LaunchableRules|NotLaunchableHint' -v` — Expected: FAIL to compile (types undefined).

- [ ] **Step 3: Implement** (`modelrows.go`)

```go
package tui

import (
	"fmt"
	"sort"

	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/localgate"
	"github.com/ohanaverse/local-ai-setup/wt/internal/localmodels"
	"github.com/ohanaverse/local-ai-setup/wt/internal/survey"
	"github.com/ohanaverse/local-ai-setup/wt/internal/usage"
)

type rowStatus string

const (
	statusOK     rowStatus = "ok"     // on disk (local) or simply available (cloud)
	statusAbsent rowStatus = "absent" // configured local model with no artifact
	statusNew    rowStatus = "new"    // discovered, unregistered
)

// tableRow is one line of the selector table before rendering.
type tableRow struct {
	model      config.Model
	location   config.Location
	status     rowStatus
	exposed    bool
	running    bool
	discovered bool
	counts     usage.UsageCounts
	stats      survey.Stats
}

// tableInput gathers everything buildRows needs. models is the agent's
// PRE-GATE eligible list (exposed cloud + every configured local model,
// already filtered by agent/-T/-F); inventory is nil when no local probe ran
// (running/absent are then unknown: locals read as not running, status ok).
type tableInput struct {
	cfg            *config.Config
	agent          string
	models         []config.Model
	inventory      *localmodels.Snapshot
	hideDiscovered bool
	usage          usage.Store
	stats          map[string]survey.Stats
}

func buildRows(in tableInput) []tableRow {
	byID := map[string]localmodels.Entry{}
	if in.inventory != nil {
		for _, e := range in.inventory.Entries {
			byID[e.ModelID] = e
		}
	}
	rows := make([]tableRow, 0, len(in.models))
	seen := map[string]bool{}
	for _, m := range in.models {
		loc := m.Location
		if in.cfg != nil {
			if l, err := in.cfg.ResolveLocation(m); err == nil {
				loc = l
			}
		}
		r := tableRow{model: m, location: loc, status: statusOK}
		if loc == config.LocationLocal && in.inventory != nil {
			if e, ok := byID[m.ID]; ok {
				r.running = e.Running
				if e.Artifact == "" {
					r.status = statusAbsent
				}
			} else {
				r.status = statusAbsent
			}
		}
		r.exposed = m.Native || (in.cfg != nil && in.cfg.ExposedFlag(m.ID))
		rows = append(rows, r)
		seen[m.ID] = true
	}
	if in.inventory != nil && !in.hideDiscovered {
		for _, e := range in.inventory.Entries {
			if e.Registered || seen[e.ModelID] {
				continue
			}
			if in.agent != "" && (in.cfg == nil || !in.cfg.AgentSupportsProvider(in.agent, e.ProviderID)) {
				continue
			}
			rows = append(rows, tableRow{
				model: config.Model{
					ID: e.ModelID, ProviderID: e.ProviderID, ModelName: e.Artifact,
					Location: config.LocationLocal, Source: config.SourceDiscovered,
				},
				location: config.LocationLocal, status: statusNew, running: e.Running, discovered: true,
			})
			seen[e.ModelID] = true
		}
	}

	ids := make([]string, len(rows))
	for i, r := range rows {
		ids[i] = r.model.ID
	}
	var counts map[string]usage.UsageCounts
	if in.usage != nil {
		if in.agent != "" {
			counts = in.usage.CountsForAgent(in.agent, ids)
		} else {
			counts = in.usage.Counts(ids)
		}
	}
	for i := range rows {
		rows[i].counts = counts[rows[i].model.ID]
		rows[i].stats = in.stats[rows[i].model.ID]
	}
	return rows
}

// costKey is the sort key for "cost ascending": output price per million,
// then input price per million. Local models and subscription-only models
// (no per-token prices) count as $0; a model with no cost data at all sorts
// after every priced model.
type costKey struct {
	noData  bool
	out, in float64
}

func rowCostKey(r tableRow) costKey {
	if r.location == config.LocationLocal {
		return costKey{}
	}
	c := r.model.Cost
	if c.InputPricePerMillion == nil && c.CachePricePerMillion == nil && c.OutputPricePerMillion == nil {
		if c.SubscriptionPrice != nil {
			return costKey{}
		}
		return costKey{noData: true}
	}
	var k costKey
	if c.OutputPricePerMillion != nil {
		k.out = *c.OutputPricePerMillion
	}
	if c.InputPricePerMillion != nil {
		k.in = *c.InputPricePerMillion
	}
	return k
}

// sortRows orders rows in place: group 1 (cloud + running local) by cost
// ascending then 7-day usage ascending then id; group 2 (non-running local)
// alphabetical by id.
func sortRows(rows []tableRow) {
	group1 := func(r tableRow) bool { return r.location != config.LocationLocal || r.running }
	sort.SliceStable(rows, func(i, j int) bool {
		a, b := rows[i], rows[j]
		ga, gb := group1(a), group1(b)
		if ga != gb {
			return ga
		}
		if !ga {
			return a.model.ID < b.model.ID
		}
		ka, kb := rowCostKey(a), rowCostKey(b)
		if ka.noData != kb.noData {
			return !ka.noData
		}
		if ka.out != kb.out {
			return ka.out < kb.out
		}
		if ka.in != kb.in {
			return ka.in < kb.in
		}
		if a.counts.SevenDay != b.counts.SevenDay {
			return a.counts.SevenDay < b.counts.SevenDay
		}
		return a.model.ID < b.model.ID
	})
}

// launchable reports whether Enter may launch the row now: cloud always; a
// running local row; and a pulled ollama model even when not loaded, because
// ollama's daemon loads models on demand (and modelman starts ollama models
// flag-only, so gating them would regress launching them).
func (r tableRow) launchable() bool {
	if r.location != config.LocationLocal || r.running {
		return true
	}
	return r.model.ProviderID == "ollama" && r.status != statusAbsent
}

// notLaunchableHint is the status line shown when Enter lands on a row that
// cannot launch yet.
func (r tableRow) notLaunchableHint() string {
	if r.discovered {
		return fmt.Sprintf("%s is not running — start it with the %s CLI", r.model.ID, r.model.ProviderID)
	}
	return (&localgate.NotRunningError{ModelID: r.model.ID}).Error()
}
```

- [ ] **Step 4: Run to verify pass**

Run: `go test ./internal/tui -run 'BuildRows|SortRowsTwoGroups|LaunchableRules|NotLaunchableHint' -v && go vet ./...` — Expected: PASS.

- [ ] **Step 5: Commit**

```bash
gofmt -l internal
git add wt/internal/tui/modelrows.go wt/internal/tui/modelrows_test.go
git commit -m "feat(tui): selector rows layer - build, two-group sort, launchable - completes plan item #2"
```

---

### Task 3: Table renderer — header, aligned rows, `buildTable`

**Files:**
- Create: `wt/internal/tui/modeltable.go`, `wt/internal/tui/modeltable_test.go`
- Modify: `wt/internal/tui/model_list.go` (`modelItem`: add fields)

**Interfaces:**
- Consumes: `tableRow`/`tableInput`/`buildRows`/`sortRows`/`launchable`/`notLaunchableHint` (Task 2); `formatPerToken`, `modelItem.Title`, `refColumn`, `newRefcountStore`, `survey.FormatPickerSegment`, `agents.ProtocolsFor`, `cfg.ResolveRoute`, `config.ErrLitellmUnconfigured`.
- Produces:
  - `modelItem` gains `row tableRow` and `blocked string` (non-empty = Enter must show this instead of launching).
  - `type modelTable struct { header string; items []*modelItem }`
  - `func renderTable(rows []tableRow, cfg *config.Config, agent string, refs map[string]int, lastID string) modelTable` (rows must already be sorted)
  - `func buildTable(in tableInput, refs refcount.Store, lastID string) modelTable` — `buildRows` + `sortRows` + ref counts + `renderTable`. `refs` may be nil.

- [ ] **Step 1: Write the failing tests** (`modeltable_test.go`)

```go
package tui

import (
	"strings"
	"testing"

	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/usage"
)

func tableTestRows() []tableRow {
	return []tableRow{
		{model: config.Model{ID: "openrouter/cheap", Family: "kimi", Cost: config.ModelCost{InputPricePerMillion: f64(0.5), OutputPricePerMillion: f64(2)}},
			location: config.LocationCloud, status: statusOK, exposed: true, counts: usage.UsageCounts{OneDay: 1, SevenDay: 12, ThirtyDay: 340}},
		{model: config.Model{ID: "omlx/Qwen3.8-27B-4bit", Family: "qwen"}, location: config.LocationLocal, status: statusOK, running: true},
		{model: config.Model{ID: "omlx/disc"}, location: config.LocationLocal, status: statusNew, discovered: true},
		{model: config.Model{ID: "omlx/gone", Family: "qwen"}, location: config.LocationLocal, status: statusAbsent},
	}
}

// TestRenderTableHeaderAndCells verifies the header carries every column
// name in order and each row renders the specified ASCII cells: LOC
// cloud/local, STATUS ok/new/absent, EXPOSED Y/-, RUNNING run/-, discovered
// rows with family "-" and cost "-".
func TestRenderTableHeaderAndCells(t *testing.T) {
	tbl := renderTable(tableTestRows(), nil, "", nil, "")
	last := -1
	for _, h := range []string{"FAMILY", "MODEL", "LOC", "STATUS", "EXPOSED", "RUNNING", "COST", "1D", "7D", "30D", "SURVEY"} {
		i := strings.Index(tbl.header, h)
		if i <= last {
			t.Fatalf("header %q: column %q out of order or missing", tbl.header, h)
		}
		last = i
	}
	cloud, run, disc, absent := tbl.items[0].line, tbl.items[1].line, tbl.items[2].line, tbl.items[3].line
	for _, want := range []string{"kimi", "openrouter/cheap", "cloud", "ok", "Y", "0.5000", "12", "340"} {
		if !strings.Contains(cloud, want) {
			t.Errorf("cloud line %q missing %q", cloud, want)
		}
	}
	if !strings.Contains(run, "local") || !strings.Contains(run, "run") {
		t.Errorf("running line = %q", run)
	}
	if !strings.HasPrefix(disc, "-") || !strings.Contains(disc, "new") {
		t.Errorf("discovered line = %q (want family '-' and status new)", disc)
	}
	if !strings.Contains(absent, "absent") {
		t.Errorf("absent line = %q", absent)
	}
}

// TestRenderTableColumnsAlign verifies the header and every row share column
// offsets: each heading starts at the same rune offset as the value beneath
// it. This is what makes the header trustworthy — a drift would mislabel
// every column.
func TestRenderTableColumnsAlign(t *testing.T) {
	tbl := renderTable(tableTestRows(), nil, "", nil, "")
	runes := func(s string) []rune { return []rune(s) }
	hdr := runes(tbl.header)
	// header includes the 4-rune prefix (ref column + marker) that Title() adds to rows
	col := func(name string) int { return len(runes(tbl.header[:strings.Index(tbl.header, name)])) }
	line := func(i int) []rune { return runes(strings.Repeat(" ", 4) + tbl.items[i].line) }
	if got := string(line(0)[col("LOC") : col("LOC")+5]); got != "cloud" {
		t.Errorf("LOC cell = %q", got)
	}
	if got := string(line(1)[col("RUNNING") : col("RUNNING")+3]); got != "run" {
		t.Errorf("RUNNING cell = %q", got)
	}
	if got := string(line(0)[col("EXPOSED")]); got != "Y" {
		t.Errorf("EXPOSED cell = %q", got)
	}
	if got := string(line(3)[col("STATUS") : col("STATUS")+6]); got != "absent" {
		t.Errorf("STATUS cell = %q", got)
	}
	_ = hdr
}

// TestRenderTableSurveySegmentTrailing verifies the survey segment is appended
// last and omitted when nothing is answered, so it never shifts a column.
func TestRenderTableSurveySegmentTrailing(t *testing.T) {
	rows := tableTestRows()
	rows[0].stats.Answered, rows[0].stats.Worked = 5, 4
	tbl := renderTable(rows, nil, "", nil, "")
	if !strings.Contains(tbl.items[0].line, "n5") {
		t.Errorf("survey segment missing: %q", tbl.items[0].line)
	}
	if strings.Contains(tbl.items[1].line, "n0") || strings.HasSuffix(tbl.items[1].line, " ") {
		t.Errorf("no-answer row has stray segment/trailing space: %q", tbl.items[1].line)
	}
}

// TestRenderTableBlockedAndMarkers verifies items carry the not-launchable
// hint for a non-running omlx row, mark the last-launched row, and pick up
// the in-use ref count.
func TestRenderTableBlockedAndMarkers(t *testing.T) {
	tbl := renderTable(tableTestRows(), nil, "", map[string]int{"openrouter/cheap": 2}, "omlx/Qwen3.8-27B-4bit")
	if tbl.items[0].blocked != "" || tbl.items[1].blocked != "" {
		t.Error("cloud and running rows must not be blocked")
	}
	if tbl.items[2].blocked == "" || tbl.items[3].blocked == "" {
		t.Error("non-running omlx rows must carry a hint")
	}
	if !tbl.items[1].marked || tbl.items[0].marked {
		t.Error("only the last-launched row is marked")
	}
	if tbl.items[0].ref != 2 {
		t.Errorf("ref = %d, want 2", tbl.items[0].ref)
	}
}

// TestRenderTableDiscoveredBlockedUnderLitellm verifies a discovered model —
// which is not in LiteLLM's model_list — is unselectable and labelled when
// LiteLLM routing is on, but fine in direct mode.
func TestRenderTableDiscoveredBlockedUnderLitellm(t *testing.T) {
	cfg := &config.Config{
		Providers: []config.Provider{{ID: "omlx", Location: config.LocationLocal, Protocols: []config.Protocol{config.ProtocolOpenAIChat}}},
		Agents:    []config.Agent{{Name: "opencode", SupportedProviders: []string{"omlx"}}},
	}
	row := tableRow{model: config.Model{ID: "omlx/disc", ProviderID: "omlx", ModelName: "disc", Location: config.LocationLocal}, location: config.LocationLocal, status: statusNew, running: true, discovered: true}

	direct := renderTable([]tableRow{row}, cfg, "opencode", nil, "")
	if direct.items[0].blocked != "" || direct.items[0].exception != "" {
		t.Errorf("direct mode: blocked=%q exception=%q, want none", direct.items[0].blocked, direct.items[0].exception)
	}
	cfg.SetLitellmForTest(true, "http://localhost:4000", "sk-test")
	lite := renderTable([]tableRow{row}, cfg, "opencode", nil, "")
	if lite.items[0].exception != "(not in LiteLLM)" || lite.items[0].blocked == "" {
		t.Errorf("litellm mode: exception=%q blocked=%q", lite.items[0].exception, lite.items[0].blocked)
	}
}
```
(`SetLitellmForTest` may not exist: the implementer must find how existing tests enable LiteLLM on a `Config` — see `TestBuildModelItemsLitellmRequiredLabel` / `ViaProxyLabelUnaffected` in `model_list_test.go` — and use that mechanism; if no setter exists, use whatever helper those tests use to build a litellm-on config. Do not add production code just for this test.)

- [ ] **Step 2: Run to verify failure**

Run: `go test ./internal/tui -run 'RenderTable' -v` — Expected: FAIL to compile.

- [ ] **Step 3: Implement**

`model_list.go` — extend `modelItem`:
```go
type modelItem struct {
	model     config.Model
	line      string
	marked    bool
	ref       int
	exception string
	row       tableRow // table row this item renders (zero for legacy items)
	blocked   string   // non-empty: Enter shows this instead of launching
}
```
`modeltable.go`:
```go
package tui

import (
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/ohanaverse/local-ai-setup/wt/internal/agents"
	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/refcount"
	"github.com/ohanaverse/local-ai-setup/wt/internal/survey"
)

// modelTable is the rendered selector: a header line (shown as the list's
// title) and one item per sorted row.
type modelTable struct {
	header string
	items  []*modelItem
}

const rowPrefixWidth = 4 // ref column (2) + rotation marker (2), composed by modelItem.Title()

func padRunes(s string, w int) string {
	if n := utf8.RuneCountInString(s); n < w {
		return s + strings.Repeat(" ", w-n)
	}
	return s
}

func maxRunes(min int, ss ...string) int {
	w := min
	for _, s := range ss {
		if n := utf8.RuneCountInString(s); n > w {
			w = n
		}
	}
	return w
}

func flag(b bool, yes string) string {
	if b {
		return yes
	}
	return "-"
}

// buildTable is the one entry point the pickers use: build rows, sort them,
// look up in-use counts, render.
func buildTable(in tableInput, refs refcount.Store, lastID string) modelTable {
	rows := buildRows(in)
	sortRows(rows)
	var refCounts map[string]int
	if refs != nil {
		ids := make([]string, len(rows))
		for i, r := range rows {
			ids[i] = r.model.ID
		}
		refCounts = refs.Counts(ids)
	}
	return renderTable(rows, in.cfg, in.agent, refCounts, lastID)
}

// renderTable formats already-sorted rows. Columns are separated by two
// spaces and padded to the widest cell (headings included) so the header and
// every row line up; measured in runes so multi-byte names don't shift them.
func renderTable(rows []tableRow, cfg *config.Config, agent string, refs map[string]int, lastID string) modelTable {
	cost := make([]string, len(rows))
	fam := make([]string, len(rows))
	c1, c7, c30 := make([]string, len(rows)), make([]string, len(rows)), make([]string, len(rows))
	famW, idW, costW, w1, w7, w30 := len("FAMILY"), len("MODEL"), len("COST"), len("1D"), len("7D"), len("30D")
	for i, r := range rows {
		fam[i] = r.model.Family
		if fam[i] == "" {
			fam[i] = "-"
		}
		cost[i] = "-"
		if !r.discovered {
			cost[i] = formatPerToken(r.model.Cost)
		}
		c1[i], c7[i], c30[i] = fmt.Sprint(r.counts.OneDay), fmt.Sprint(r.counts.SevenDay), fmt.Sprint(r.counts.ThirtyDay)
		famW = maxRunes(famW, fam[i])
		idW = maxRunes(idW, r.model.ID)
		costW = maxRunes(costW, cost[i])
		w1, w7, w30 = maxRunes(w1, c1[i]), maxRunes(w7, c7[i]), maxRunes(w30, c30[i])
	}
	const sep = "  "
	header := strings.Repeat(" ", rowPrefixWidth) + strings.Join([]string{
		padRunes("FAMILY", famW), padRunes("MODEL", idW), padRunes("LOC", 5), padRunes("STATUS", 6),
		padRunes("EXPOSED", 7), padRunes("RUNNING", 7), padRunes("COST", costW),
		padRunes("1D", w1), padRunes("7D", w7), padRunes("30D", w30), "SURVEY",
	}, sep)

	items := make([]*modelItem, 0, len(rows))
	for i, r := range rows {
		loc := string(r.location)
		if loc == "" {
			loc = "-"
		}
		line := strings.Join([]string{
			padRunes(fam[i], famW), padRunes(r.model.ID, idW), padRunes(loc, 5), padRunes(string(r.status), 6),
			padRunes(flag(r.exposed, "Y"), 7), padRunes(flag(r.running, "run"), 7), padRunes(cost[i], costW),
			padRunes(c1[i], w1), padRunes(c7[i], w7), padRunes(c30[i], w30),
		}, sep)
		if seg := survey.FormatPickerSegment(r.stats); seg != "" {
			line += sep + seg
		}
		it := &modelItem{model: r.model, line: line, marked: lastID != "" && r.model.ID == lastID, ref: refs[r.model.ID], row: r}
		if !r.launchable() {
			it.blocked = r.notLaunchableHint()
		}
		if cfg != nil {
			route, err := cfg.ResolveRoute(r.model, agents.ProtocolsFor(agent))
			switch {
			case r.discovered && (err != nil || route.Litellm || route.Forced):
				// A discovered model is not in LiteLLM's model_list: routing
				// it through the proxy cannot work, so it is unselectable.
				it.exception = "(not in LiteLLM)"
				it.blocked = "discovered model " + r.model.ID + " is not in LiteLLM — turn LiteLLM routing off (modelman litellm off) to use it"
			case errors.Is(err, config.ErrLitellmUnconfigured):
				it.exception = "(litellm required)"
			case err != nil:
				it.exception = "(unavailable)"
			case route.Forced:
				it.exception = "(via proxy)"
			}
		}
		items = append(items, it)
	}
	return modelTable{header: header, items: items}
}
```

- [ ] **Step 4: Run to verify pass**

Run: `go test ./internal/tui -run 'RenderTable|BuildRows|SortRows|Launchable|NotLaunchable' -v && go build ./... && go vet ./...` — Expected: PASS (the legacy builder and its tests are untouched and still pass).

- [ ] **Step 5: Commit**

```bash
gofmt -l internal
git add wt/internal/tui
git commit -m "feat(tui): selector table renderer - header, aligned rows, buildTable - completes plan item #3"
```

---

### Task 4: Wire the table into the agent flow (`enterModelPhase`)

**Files:**
- Modify: `wt/internal/tui/app.go` (`enterModelPhase` ~line 680; phaseModel Enter handler ~line 393)
- Modify: `wt/internal/tui/testhelpers_test.go` (add `TestMain` inventory stub + `stubInventory`)
- Modify/replace tests: `wt/internal/tui/local_gate_test.go`, plus any test in `app_test.go` / `agent_model_test.go` / `model_family_test.go` (`TestEnterModelPhaseCursorOnFirstModel`) that the change breaks.

**Interfaces:**
- Consumes: `buildTable`, `modelTable`, `tableInput` (Tasks 2-3), `localmodels.Inventory`, `localgate.Apply` (pin check only), `rotation`, `survey.AgentModelStats`.
- Produces: `var runInventory = localmodels.Inventory` seam; `enterModelPhase` builds the model list from the table; `m.models` list items are the table's `*modelItem`s with the header as the list title; Enter on an item with `blocked != ""` sets `m.status` and does not launch.

- [ ] **Step 1: Write the failing tests**

Add to `testhelpers_test.go`:
```go
// TestMain stubs the live-provider inventory so no test in this package ever
// probes the developer's real ollama/omlx/mtplx servers or reads their model
// directories. Tests that need local rows call stubInventory.
func TestMain(m *testing.M) {
	runInventory = func(*config.Config) localmodels.Snapshot { return localmodels.Snapshot{} }
	os.Exit(m.Run())
}

// stubInventory makes enterModelPhase see snap for the duration of a test.
func stubInventory(t *testing.T, snap localmodels.Snapshot) {
	t.Helper()
	old := runInventory
	runInventory = func(*config.Config) localmodels.Snapshot { return snap }
	t.Cleanup(func() { runInventory = old })
}
```
New tests in `modeltable_flow_test.go` (create) — use the existing fixtures `gateTestConfig()` (see `local_gate_test.go`) and `model{cfg:..., width:80, height:24}`:
```go
// TestEnterModelPhaseShowsNonRunningAndDiscoveredRows verifies the picker now
// lists a configured local model that is not running and a discovered model
// alongside cloud models, instead of hiding local models the way the old gate
// did — the point of the new table.
func TestEnterModelPhaseShowsNonRunningAndDiscoveredRows(t *testing.T) { ... }

// TestEnterModelPhaseHeaderIsListTitle verifies the column header is the
// model list's title (so it renders above the rows) and starts with the
// FAMILY heading after the 4-rune row prefix.
func TestEnterModelPhaseHeaderIsListTitle(t *testing.T) { ... }

// TestEnterOnNonRunningRowShowsHintAndDoesNotLaunch verifies Enter on a
// non-running omlx row sets the start hint in m.status and leaves the phase at
// phaseModel (no launch, no rotation/usage write).
func TestEnterOnNonRunningRowShowsHintAndDoesNotLaunch(t *testing.T) { ... }

// TestEnterModelPhaseHidesDiscoveredWhenFiltered verifies -T/-F (activeTags /
// activeFamily) drop discovered rows, which have no tags or family.
func TestEnterModelPhaseHidesDiscoveredWhenFiltered(t *testing.T) { ... }

// TestEnterModelPhaseSingleLaunchableRowAutoLaunches verifies the
// one-row shortcut still applies (exactly one row that is launchable), and
// does NOT apply when the only rows are non-launchable (the table shows so the
// hint is visible).
func TestEnterModelPhaseSingleRowShortcut(t *testing.T) { ... }

// TestEnterModelPhasePinnedStillUsesLocalGate verifies a -M pin naming a local
// model that localgate does not verify still routes back to the agent picker
// (non-TUI parity), while a pinned cloud model launches.
// (This is the existing TestEnterModelPhasePinnedStaleLocalModelRejected —
// keep it, do not duplicate.)
```
Write each with real bodies following the fixtures in `local_gate_test.go` (build a `Snapshot` with `localmodels.Entry` values and call `stubInventory`); assertions:
- Rows test: `got.models.Items()` IDs include the cloud model, the configured non-running local model, and `config.DiscoveredModelID(...)` for the discovered entry.
- Header test: `got.models.Title` starts with 4 spaces then `FAMILY`.
- Enter test: send `tea.KeyMsg{Type: tea.KeyEnter}` via `got.Update` with the non-running row selected (`got.models.Select(idx)`); assert `got.phase == phaseModel` and `strings.Contains(got.status, "modelman start")`.
- Filter test: set `m.activeTags = "code"`, expect no discovered row.
- Shortcut test: config with a single cloud model and empty inventory → phase moves past model (`phaseResume`/launch cmd path as the existing single-model test asserts — copy that assertion from the current single-model test).

- [ ] **Step 2: Run to verify failure**

Run: `go test ./internal/tui -run 'EnterModelPhaseShowsNonRunning|HeaderIsListTitle|NonRunningRowShowsHint|HidesDiscoveredWhenFiltered|SingleRowShortcut' -v` — Expected: FAIL (compile error `runInventory` undefined, then behavior).

- [ ] **Step 3: Implement**

In `app.go` (imports: add `localmodels`; drop unused `familyOf` code):
```go
// runInventory is a test seam: production probes the live local providers.
var runInventory = localmodels.Inventory
```
Rewrite `enterModelPhase` (keep its doc comment, updated) as:
```go
func (m model) enterModelPhase(agent string, models, fullCatalog []config.Model, firstTag string) (model, tea.Cmd) {
	m.tag = firstTag

	routeBack := func(status string) (model, tea.Cmd) { /* unchanged body */ }

	// A -M pin keeps localgate's flag+probe verdict, so the TUI and the
	// non-TUI path agree on whether a pinned local model may launch.
	var snap *localmodels.Snapshot
	if m.pinnedModel != "" {
		gate := localgate.Apply(m.cfg, models, m.pinnedModel)
		if gate.PinnedRejected != nil {
			return routeBack(gate.PinnedRejected.Error())
		}
		if config.IndexModelByID(gate.Eligible, m.pinnedModel) < 0 {
			return routeBack(fmt.Sprintf("model %q is not in the eligible list for agent %q", m.pinnedModel, agent))
		}
		models = gate.Eligible
	} else {
		s := runInventory(m.cfg)
		snap = &s
	}

	rot := rotation.New()
	lastID, _ := rot.Last()
	surveyStats := survey.AgentModelStats(newSurveyStore().Events(), agent, survey.Window30d, time.Now().UTC())
	tbl := buildTable(tableInput{
		cfg: m.cfg, agent: agent, models: models, inventory: snap,
		hideDiscovered: m.activeTags != "" || m.activeFamily != "",
		usage: newUsageStore(), stats: surveyStats,
	}, newRefcountStore(), lastID)
	if len(tbl.items) == 0 {
		return routeBack(fmt.Sprintf("no models for agent %q — edit your config", agent))
	}

	delegate := ThemedListDelegate(m.theme)
	delegate.ShowDescription = false
	delegate.SetSpacing(0)
	listItems := make([]list.Item, len(tbl.items))
	idIndex := make(map[string]int, len(tbl.items))
	for i, it := range tbl.items {
		listItems[i] = it
		idIndex[it.model.ID] = i
	}
	ml := list.New(listItems, delegate, m.width-2, m.height-2)
	ml.Title = tbl.header
	// The header must sit flush with the row text: drop the default title
	// padding/background so its first column starts where item text does.
	ml.Styles.Title = lipgloss.NewStyle().Foreground(m.theme.Token(themes.TokenDim))
	ml.SetShowStatusBar(false)
	m.models = ml

	if m.pinnedModel != "" {
		if idx, ok := idIndex[m.pinnedModel]; ok {
			m.models.Select(idx)
		}
		return m.proceedToLaunch()
	}

	// Cursor: the rotation's next-to-use model among launchable rows,
	// otherwise the first launchable row, otherwise the top.
	var launchable []config.Model
	first := -1
	for i, it := range tbl.items {
		if it.blocked == "" {
			launchable = append(launchable, it.model)
			if first < 0 {
				first = i
			}
		}
	}
	pos := 0
	if first >= 0 {
		pos = first
	}
	if next, ok := rot.NextFromEligible(launchable, m.cfg); ok {
		if idx, ok := idIndex[next.ID]; ok {
			pos = idx
		}
	}
	m.models.Select(pos)

	if len(tbl.items) == 1 && tbl.items[0].blocked == "" {
		return m.proceedToLaunch()
	}
	m.phase = phaseModel
	return m, nil
}
```
(Keep `fullCatalog`/`firstTag` parameters for the callers' sake; `fullCatalog` may now be unused — keep the parameter and use `_ = fullCatalog` only if the compiler complains; better, leave the signature and note the parameter is retained for callers. Add the `lipgloss` import if missing.)

phaseModel Enter handler — right after `highlighted, ok := m.models.SelectedItem().(*modelItem)` / `!ok` guard:
```go
				if highlighted.blocked != "" {
					m.status = highlighted.blocked
					return m, nil
				}
```

- [ ] **Step 4: Migrate the tests this breaks**

Run `go test ./internal/tui` and resolve every failure by these dispositions (each kept test keeps its original what/why comment, updated where the behavior description changed):

| Existing test | Disposition |
|---|---|
| `TestEnterModelPhaseNoMarkerHidesLocalModels` | REWRITE: with an empty inventory the configured local model now appears as a non-running row (blocked hint), not hidden; assert 2 rows and the local one `blocked != ""`. Rename to `...ShowsLocalModelsAsNonRunning`. |
| `TestEnterModelPhaseVerifiedMarkerShowsLocalModel` | REWRITE to stub `Inventory` with `Running: true` for that model (no httptest needed); assert it is a launchable row. |
| `TestEnterModelPhaseDriftedMarkerDropsOnlyThatModel` | REWRITE: inventory says one local model running, one not; assert running one launchable, other blocked. |
| `TestEnterModelPhasePinnedStaleLocalModelRejected` | KEEP (pin check still uses `localgate.Apply`). |
| `TestEnterModelPhaseGateEmptiedRoutesBack` | REPLACE with a test that "all eligible models local, none running" now shows the table with every row blocked (no route back). |
| `TestEnterModelPhasePinnedNotInEligibleRoutesBack` | KEEP. |
| `TestEnterModelPhaseCursorOnFirstModel` (model_family_test.go) | ADAPT: cursor lands on the first launchable row of the new sort. |
| Any test asserting the old `line` format or `[tags]` | ADAPT to the new columns. |
Do not weaken assertions to make them pass; if a test's behavior is genuinely obsolete, replace it with an equivalent for the new behavior and say so in the report.

- [ ] **Step 5: Run to verify pass, then commit**

Run: `go build ./... && go vet ./... && go test -count=1 ./...` — Expected: PASS.
```bash
gofmt -l internal
git add wt/internal/tui
git commit -m "feat(tui): agent-flow model picker shows the selector table - completes plan item #4"
```

---

### Task 5: `PickModel` uses the table; retire the legacy builder

**Files:**
- Modify: `wt/internal/tui/pick_model.go`, `wt/internal/tui/model_list.go` (delete `buildModelItems`, `sortModelsByUsage`; keep `modelItem`, `Title`, `refColumn`, `formatPerToken`, markers, `clampModelSelection`, `phaseModelView`), `wt/internal/tui/testhelpers_test.go` (`compactModelList` → table-based), `wt/internal/tui/model_family_test.go`, `wt/internal/tui/model_list_test.go`, `wt/internal/tui/model_line_test.go`, `wt/internal/tui/pick_model_test.go`, `wt/internal/tui/agent_model_test.go`, `wt/internal/tui/app_test.go`

**Interfaces:**
- Consumes: `buildTable`, `modelTable`, `tableInput`, `runInventory`.
- Produces: `newPickModel(cfg, models, theme)` builds items via `buildTable` with `agent: ""`, `hideDiscovered: true`, inventory from `runInventory(cfg)` when cfg != nil; title = table header (same styling as Task 4). `PickModel`'s exported signature is unchanged (`cmd/wt/smoke.go` untouched).

- [ ] **Step 1: Write the failing test** (`pick_model_test.go`)

```go
// TestNewPickModelUsesTableHeaderAndRunningState verifies wt smoke's picker
// shows the same column header as the agent flow and reads RUNNING from the live
// inventory (a running local model shows "run"), with no per-agent counts since
// the list is not scoped to one agent.
func TestNewPickModelUsesTableHeaderAndRunningState(t *testing.T) { ... }
```
Body: `stubInventory(t, Snapshot{Entries: []Entry{{ProviderID:"omlx", ModelID:"omlx/a", Artifact:"a", Registered:true, Running:true}}})`; models `[]config.Model{{ID:"omlx/a", ProviderID:"omlx", ModelName:"a"}}` with a cfg containing the omlx provider; `pm := newPickModel(cfg, models, themes.Default)`; assert `strings.Contains(pm.list.Title, "RUNNING")` and the single item's line contains `run`.

- [ ] **Step 2: Run to verify failure**

Run: `go test ./internal/tui -run TestNewPickModelUsesTableHeaderAndRunningState -v` — Expected: FAIL (title is "Pick a model").

- [ ] **Step 3: Implement**

`pick_model.go`: replace the `familyOf`/`buildModelItems` block with:
```go
	var snap *localmodels.Snapshot
	if cfg != nil {
		s := runInventory(cfg)
		snap = &s
	}
	tbl := buildTable(tableInput{
		cfg: cfg, agent: "", models: models, inventory: snap, hideDiscovered: true,
		usage: newUsageStore(),
	}, newRefcountStore(), "")
	items := tbl.items
```
and `l.Title = tbl.header` with the same `l.Styles.Title` override as Task 4 (extract a small helper `styleTableTitle(l *list.Model, theme themes.Theme)` used by both). Update the `pickModel` doc comment (no longer "same rows buildModelItems produces"). In `pickModel.Update`'s Enter: also refuse a `blocked` item? No — wt smoke passes only models it already verified eligible; leave unchanged.

Delete `buildModelItems` and `sortModelsByUsage` from `model_list.go` (and the unused `usage`/`refcount`/`strings` imports it leaves). Leave `usage.CompositeScore` and `usage.AggregateByFamily` in `internal/usage` (exported, separately tested; record a follow-up to remove if nothing else uses them).

- [ ] **Step 4: Migrate remaining tests**

Update `testhelpers_test.go`: `compactModelList(t, models)` now builds via `buildTable(tableInput{models: models, usage: newUsageStore()}, newRefcountStore(), "")` and sets `ml.Title = tbl.header`; add
```go
// tableItems builds picker items the way production does, for tests.
func tableItems(t *testing.T, cfg *config.Config, agent string, models []config.Model, snap *localmodels.Snapshot) modelTable {
	t.Helper()
	return buildTable(tableInput{cfg: cfg, agent: agent, models: models, inventory: snap, usage: newUsageStore()}, newRefcountStore(), "")
}
```
Then fix each remaining failure by these dispositions:

| Tests | Disposition |
|---|---|
| `TestSortModelsByUsageGroupsByFamilyScoreThenModelScore`, `TestSortModelsByUsageKeepsTiedFamiliesAdjacent`, `TestAdjacentModelsShareFamilyColumn`, `TestBuildModelItemsFamilyColumnShowsFamilyTotal`, `TestBuildModelItemsFamilyCountsUseFullCatalog`, `TestBuildModelItemsEmptyFamilyShowsAggregate`, `TestNewPickModelFamilyTotalsCoverFullCatalog` | DELETE — the family-total column and family-usage sort are removed by the spec; Task 2's `TestSortRowsTwoGroups` and Task 3's table tests replace them. |
| `TestClampOnFilterNarrow`, `TestFilterToSingleMatchKeepsSelectionValid`, `TestWrapAroundStaysValidAfterFilterApplied`, `TestWrapAroundNoOpWhenZeroOrOneVisibleItem` | KEEP; only the fixture helper changes (`compactModelList`). |
| `TestBuildModelItemsMarksLastLaunchedRow`, `...NoMarkerWithoutLastLaunched`, `...RefColumn*`, `...MarksOnlyDeviatingRows`, `...LitellmRequiredLabel`, `...UnavailableLabelForUnknownProvider`, `...ViaProxyLabelUnaffected` | ADAPT to `buildTable`/`renderTable` (`lastID` and refs parameters), keep assertions on marker / ref / exception behavior; rename `BuildModelItems` -> `BuildTable`. |
| `TestBuildModelItemsAppendsSurveySegment`, `...OmitsSurveySegmentWhenNoAnswered` | ADAPT (survey stats via `tableInput.stats`); the new equivalents in Task 3 may make some redundant — delete a test only if an equivalent already exists, and say so in the report. |
| `TestModelItemLineFormat`, `TestModelItemDescriptionEmptyCountsInLine`, `TestModelItemLinePricingAfterUsageCounts`, `TestModelItemLinePartialPerTokenPricing`, `TestModelItemLine*` | ADAPT to the new column layout: keep the pricing-format assertions (`formatPerToken` output, `-------` placeholders) against the COST cell and the counts assertions against the 1D/7D/30D cells. |

Do not weaken assertions; if a test is obsolete, replace it with an equivalent for the new behavior, or delete it per the table, and list every deletion in the report.

- [ ] **Step 5: Run to verify pass, then commit**

Run: `go build ./... && go vet ./... && go test -count=1 ./... && gofmt -l .` — Expected: all pass; `gofmt -l` prints nothing. Also run `go test ./cmd/wt -run Smoke -count=1` to confirm `wt smoke` is unaffected.
```bash
git add wt/internal/tui
git commit -m "feat(tui): PickModel uses the selector table; retire the family-usage builder - completes plan item #5"
```

---

### Task 6: Documentation

**Files:**
- Modify: `wt/CLAUDE.md`

- [ ] **Step 1: Edit `wt/CLAUDE.md`**
  - Rotation section: the paragraph describing the picker's family-then-model `CompositeScore` sort, `AggregateByFamily`, and the compact family line is obsolete — replace with the table: columns, the two-group sort (cost ascending by output then input price, local and subscription-only = $0, no-data last; then 7d usage ascending; non-running local alphabetical), per-agent counts via `CountsForAgent`, header as the list title.
  - Session survey section: the "Model picker" bullet (trailing `✓<pct> q<quality> s<speed> n<answered>` segment) stays; note it is the SURVEY column.
  - Local-model gate section: the TUI picker no longer hides non-running local models — it lists them (STATUS/RUNNING columns), gates launch via `tableRow.launchable` (running, or pulled ollama), and takes RUNNING from `localmodels.Inventory`; `localgate.Apply` remains for the non-TUI path and the `-M` pin check.
  - Package table: `internal/tui/` row — mention `modelrows.go` (rows, sort, launchable) and `modeltable.go` (header + aligned rendering, `buildTable`), and the `runInventory` seam; add `runInventory` to the "Test seams" list; note the package's `TestMain` stubs it.
  - Exposure predicate paragraph: add that the EXPOSED column reads `Config.ExposedFlag` (raw modelman flag), unlike `IsExposed`.

- [ ] **Step 2: Verify**

Run (repo root): `make check-links && git grep -n "runInventory\|ExposedFlag\|modeltable" wt/CLAUDE.md` — Expected: links OK; matches present.

- [ ] **Step 3: Final verification**

Run (from `wt/`): `go build ./... && go vet ./... && go test -count=1 ./... && gofmt -l .` — Expected: all pass, `gofmt -l` empty.

- [ ] **Step 4: Commit**

```bash
git add wt/CLAUDE.md
git commit -m "docs(wt): document the selector table - completes plan item #6"
```

---

## Self-Review

- **Spec coverage:** columns/headings/ASCII cells -> Task 3; three row sources, agent filter, `-T/-F` hiding discovered, id-collision rule -> Task 2; sort (both groups, cost key, $0/no-data, 7d asc, alphabetical) -> Task 2; per-agent counts and survey -> Tasks 2-3; exposed via raw flag -> Task 1; launch rules (running, pulled ollama, hint, `(not in LiteLLM)`) -> Tasks 2-4; synchronous inventory probe -> Task 4; `wt smoke` picker -> Task 5; docs -> Task 6. Out-of-scope items (start/warn, smoke gate, non-TUI, removing `localgate`, divider row) untouched.
- **Spec-text deviations already recorded in the spec commit (54c51cf):** synchronous probe; pulled ollama launchable; `[tags]` cell dropped.
- **Known risk:** Tasks 4-5 migrate about 40 existing tests. The dispositions tables fix the intent per test; an implementer must not weaken assertions, and every deletion or replacement is listed in the task report for review.
- **Placeholders:** Task 4's new-test bodies are described by exact assertions rather than full code (they depend on the existing `gateTestConfig`/`phaseModelWithList` fixtures, which the implementer must read); Task 3's LiteLLM-on test states how to find the setter. These are the only two places the implementer supplies fixture plumbing.
- **Type consistency:** `tableRow`, `tableInput`, `modelTable`, `buildRows`, `sortRows`, `renderTable(rows, cfg, agent, refs, lastID)`, `buildTable(in, refs, lastID)`, `runInventory`, `modelItem.blocked`/`row` are named identically in every task that uses them.
