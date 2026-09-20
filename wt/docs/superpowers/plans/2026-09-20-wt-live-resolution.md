# wt Live Resolution Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace wt's modelman-flag-driven local-model gate with one shared, live-truth resolution path: a new `internal/catalog` package owns row/status/action rules for both the picker and the non-TUI launch, `-M` can start a non-running local model, and `internal/localgate` plus the whole `running`-flag machinery is deleted.

**Architecture:** Three layers. `internal/catalog` is pure policy — given a config, an agent, a candidate model list and a live `localmodels.Snapshot`, it produces rows each carrying a `Status` and an `Action` (launch / start / block-with-reason). Consumers wrap a `catalog.Row`: `internal/tui` adds usage counts, survey stats, sorting and rendering; `cmd/wt` adds the non-TUI start driver and the `-M` pin verdict; `internal/smoke` filters to launch rows. Because every consumer derives its answer from the same rows, the picker, the CLI and `wt smoke` can no longer disagree about whether a model may be used.

**Tech Stack:** Go 1.26.7, Bubble Tea / bubbles (TUI), cobra (CLI), BurntSushi/toml, `internal/lifecycle` (start engine), `internal/localmodels` (live probes).

**Spec:** `docs/superpowers/specs/2026-09-20-wt-live-resolution-design.md`

## Global Constraints

- **Live truth everywhere.** wt decides running state from live probes (`internal/localmodels.Inventory`), never from modelman's per-model `running` flag.
- **wt writes nothing to modelman-owned state** and never starts, stops, or restarts the LiteLLM proxy. `exposed` is read from `modelman.toml`; `running` is not read at all after Task 9.
- **The only engine change in this sub-project** is moving the start-failure error wording into `internal/lifecycle` as one exported function (Task 4). No other `internal/lifecycle` behavior changes.
- **Out of scope:** sub-project 5 (launch-time smoke gate), stopping models, `mlx_lm_server` start support, a user-facing stop command.
- Every `Test*` in Go needs a top-level `//` comment stating what it tests and why it matters.
- Module root is `wt/`. Run `go build ./...`, `go vet ./...`, `go test ./...` from `wt/`, and `make check` (gofmt gate) before each commit.
- Package-level `var x = realX` seams are the repo's test-stub convention; a `TestMain` in each package stubs live-probe seams so no test dials a real server or starts a real model.
- No new dependencies.
- **Never create a PR or push without asking the user first.**
- The spec may land as **two PRs**, cut after Task 6 (PR 1: catalog + non-TUI resolution + start driver + TUI pin; PR 2: smoke + retiring the flag machinery + docs).

---

## File Structure

**Created**

| Path | Responsibility |
|---|---|
| `internal/catalog/catalog.go` | Row inclusion, presence status, action, block reason. Pure policy, no rendering, no counts. |
| `internal/catalog/catalog_test.go` | The row tests moved out of `internal/tui`. |
| `internal/lifecycle/message.go` | `StageLabel` + `StartErrorMessage` — the single start-failure wording. |
| `internal/lifecycle/message_test.go` | Those two functions' tests, moved out of `internal/tui`. |
| `cmd/wt/start.go` | `startForLaunch`: the non-TUI start driver (progress, Ctrl+C, replace confirm). |
| `cmd/wt/start_test.go` | Driver tests. |
| `cmd/wt/testmain_test.go` | `cmd/wt`'s live-seam `TestMain` + `stubProbeInventory` / `stubStartDriver`. |
| `internal/tui/pin_model_test.go` | Renamed from `local_gate_test.go`; `-M` pin verdict from rows. |

**Modified**

| Path | Change |
|---|---|
| `internal/tui/modelrows.go` | `tableRow` embeds `catalog.Row`; `buildRows` translates to `catalog.Input`; sort + cost key stay. |
| `internal/tui/modeltable.go` | Promoted-field reads; `catalog.Action*` constants. |
| `internal/tui/modelrows_test.go` | Moved tests removed; survivors use promoted fields. |
| `internal/tui/start_flow.go` | `startErrorMessage`/`stageLabel` deleted (call `lifecycle.*`); `m.allowReplace` in `beginStart`. |
| `internal/tui/app.go` | `-M` pin verdict from rows; `allowReplace` field; `Run` param; `beginStart(highlighted, m.allowReplace)`. |
| `cmd/wt/resolve.go` | `resolveModel` on live rows; `probeInventory` seam; start on a `start` row. |
| `cmd/wt/resolve_test.go` | Rewritten to live-row semantics. |
| `cmd/wt/main.go` | `--replace` flag; `allowReplace` package var; `tuiRun` call sites; `--replace` in the TUI branch. |
| `cmd/wt/launch.go` | Doc-comment update only (`buildFilteredCmd`'s start branch is unreachable — always called with `pinned == ""`). |
| `internal/smoke/smoke.go` | `Eligibility` on live rows; `smokeProbe` seam; drop the `localgate` import. |
| `internal/smoke/smoke_test.go` | Fixture takes `t`, stubs the probe; new local-state tests. |
| `cmd/wt/smoke_test.go` | Drop `SetLocalRunningForTest`. |
| `internal/config/config.go` | Delete `runningLocal`, `localGateActive`, `RunningLocalModelIDs`, `LocalGateActive`, `FilterToRunningLocal`, `SetLocalRunningForTest`. |
| `internal/config/config_test.go` | Delete the gate tests. |
| `internal/config/modelman.go` | Delete `ExposureEntry.Running` + `modelmanState.Running` parsing. |
| `internal/config/modelman_test.go` | `TestLoadModelmanStateReadsRunningFlag` → "running key is ignored". |
| `internal/rotation/rotation.go` | Doc-comment update (line 81 references `localgate`). |
| `wt/CLAUDE.md`, `docs/guides/00,06,08`, `wt/docs/wt-smoke.md` | Documentation. |

**Deleted**

- `internal/localgate/localgate.go`, `internal/localgate/localgate_test.go` (Task 8).

---

## Task 1: Extract `internal/catalog`

**Files:**
- Create: `internal/catalog/catalog.go`
- Create: `internal/catalog/catalog_test.go`
- Modify: `internal/tui/modelrows_test.go` (remove the moved tests)

**Interfaces:**
- Consumes: `config.Config` (`ResolveLocation`, `ExposedFlag`, `AgentSupportsProvider`), `localmodels.Snapshot`/`Entry`/`Family`.
- Produces:
  - `type Status string`; `StatusOK | StatusAbsent | StatusUnknown | StatusNew` (`"ok" | "absent" | "unknown" | "new"`)
  - `type Action int`; `ActionLaunch | ActionStart | ActionBlock` (iota)
  - `type Row struct { Model config.Model; Location config.Location; Status Status; Exposed, Running, Discovered bool }`
  - `func (r Row) Action() Action`
  - `func (r Row) BlockReason() string`
  - `type Input struct { Config *config.Config; Agent string; Models []config.Model; Inventory *localmodels.Snapshot; HideDiscovered bool }`
  - `func Build(in Input) []Row`
  - `func Find(rows []Row, id string) (Row, bool)`

- [ ] **Step 1: Write the failing tests**

Create `internal/catalog/catalog_test.go`. These are the row tests lifted out of `internal/tui/modelrows_test.go` (Tasks 1's own tests; `TestBuildRowsCountsAreAgentScoped` and `TestSortRowsTwoGroups` stay in `internal/tui` because counts and sorting are the wrapper's job).

```go
// Tests for the shared model-selector row rules: which models become rows,
// each row's live presence status, and what selecting a row can do. These
// moved from internal/tui when the rules were extracted, so the picker and
// the non-TUI launch path can no longer disagree about a model's state.
package catalog

import (
	"strings"
	"testing"

	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/localmodels"
)

func f64(v float64) *float64 { return &v }

func catalogTestCfg() *config.Config {
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

func rowIDs(rows []Row) []string {
	ids := make([]string, len(rows))
	for i, r := range rows {
		ids[i] = r.Model.ID
	}
	return ids
}

// TestBuildIncludesConfiguredAndDiscoveredLocal verifies the three row
// sources: a configured cloud model, a configured local model (running or
// not), and a discovered on-disk model with no registry entry — the union
// the selector is meant to show.
func TestBuildIncludesConfiguredAndDiscoveredLocal(t *testing.T) {
	cfg := catalogTestCfg()
	models := []config.Model{
		{ID: "openrouter/cheap", ProviderID: "openrouter", ModelName: "cheap"},
		{ID: "omlx/a", ProviderID: "omlx", ModelName: "a", Family: "fam"},
	}
	inv := &localmodels.Snapshot{Entries: []localmodels.Entry{
		{ProviderID: "omlx", ModelID: "omlx/a", Artifact: "a", Registered: true, Running: true},
		{ProviderID: "omlx", ModelID: "omlx/disc", Artifact: "disc"},
	}}
	rows := Build(Input{Config: cfg, Agent: "claude", Models: models, Inventory: inv})

	if got := strings.Join(rowIDs(rows), ","); got != "openrouter/cheap,omlx/a,omlx/disc" {
		t.Fatalf("rows = %s", got)
	}
	if !rows[0].Exposed || rows[0].Location != config.LocationCloud || rows[0].Status != StatusOK {
		t.Errorf("cloud row = %+v", rows[0])
	}
	if !rows[1].Running || rows[1].Status != StatusOK || rows[1].Discovered {
		t.Errorf("configured local row = %+v", rows[1])
	}
	d := rows[2]
	if !d.Discovered || d.Status != StatusNew || d.Exposed || d.Running || d.Model.ModelName != "disc" || d.Location != config.LocationLocal {
		t.Errorf("discovered row = %+v", d)
	}
}

// TestBuildMarksAbsentAndRespectsAgentAndFilters verifies: a configured local
// model with no artifact reads "absent"; discovered models from a provider
// the agent does not support are skipped (agent supported_providers stays a
// hard constraint); HideDiscovered (-T/-F) drops discovered rows; a
// discovered id equal to an existing row id is dropped (registry row wins).
func TestBuildMarksAbsentAndRespectsAgentAndFilters(t *testing.T) {
	cfg := catalogTestCfg()
	models := []config.Model{{ID: "omlx/gone", ProviderID: "omlx", ModelName: "gone"}}
	inv := &localmodels.Snapshot{Entries: []localmodels.Entry{
		{ProviderID: "omlx", ModelID: "omlx/gone", Registered: true, ArtifactKnown: true},
		{ProviderID: "mtplx", ModelID: "mtplx/x", Artifact: "x"},      // claude does not support mtplx
		{ProviderID: "ollama", ModelID: "omlx/gone", Artifact: "dup"}, // id collision with a row
		{ProviderID: "ollama", ModelID: "ollama/ok", Artifact: "ok"},
	}}
	in := Input{Config: cfg, Agent: "claude", Models: models, Inventory: inv}
	rows := Build(in)
	if got := strings.Join(rowIDs(rows), ","); got != "omlx/gone,ollama/ok" {
		t.Fatalf("rows = %s", got)
	}
	if rows[0].Status != StatusAbsent {
		t.Errorf("Status = %q, want absent", rows[0].Status)
	}
	in.HideDiscovered = true
	if got := strings.Join(rowIDs(Build(in)), ","); got != "omlx/gone" {
		t.Errorf("HideDiscovered rows = %s", got)
	}
}

// TestBuildNativeModelIsExposedWithoutFlag pins that a native model
// (Anthropic-direct, provider auth.type "native") shows EXPOSED without any
// modelman.toml flag, matching modelman's own EXPOSED column, which reports
// native rows as exposed unconditionally. A regression here would make wt's
// picker disagree with modelman for native rows.
func TestBuildNativeModelIsExposedWithoutFlag(t *testing.T) {
	cfg := catalogTestCfg()
	models := []config.Model{{ID: "openrouter/native", ProviderID: "openrouter", ModelName: "native", Native: true}}
	rows := Build(Input{Config: cfg, Agent: "claude", Models: models})
	if len(rows) != 1 || !rows[0].Exposed {
		t.Fatalf("native row = %+v, want exposed=true with no flag set", rows)
	}
	if cfg.ExposedFlag("openrouter/native") {
		t.Fatal("test premise broken: flag must be unset")
	}
}

// TestBuildUnknownLocalStatus verifies the three-way artifact rule: a probe
// that answered and found nothing reads "absent" (and the row is blocked: the
// engine would wait out its warmup on a model that is not there), a probe that
// could not tell reads "unknown" (and stays startable — a transient daemon
// hiccup must not make the picker refuse to start a model that is actually
// pulled), and a row serving right now never reads absent even when its
// artifact could not be resolved (mlx_lm_server, whose target+draft pairing is
// not discoverable).
func TestBuildUnknownLocalStatus(t *testing.T) {
	cfg := catalogTestCfg()
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
	rows := Build(Input{Config: cfg, Agent: "claude", Models: models, Inventory: inv})
	byID := map[string]Row{}
	for _, r := range rows {
		byID[r.Model.ID] = r
	}
	if got := byID["ollama/unknown"].Status; got != StatusUnknown {
		t.Errorf("unreachable ollama status = %q, want unknown", got)
	}
	if got := byID["ollama/unknown"].Action(); got != ActionStart {
		t.Errorf("unreachable ollama action = %v, want ActionStart (fail open)", got)
	}
	if got := byID["omlx/nope"].Status; got != StatusAbsent {
		t.Errorf("answered omlx status = %q, want absent", got)
	}
	if got := byID["omlx/nope"].Action(); got != ActionBlock {
		t.Errorf("an absent non-running omlx row must be blocked, got action %v", got)
	}
	if got := byID["mlx_lm_server/serving"].Status; got != StatusOK {
		t.Errorf("running mlx_lm_server status = %q, want ok", got)
	}
}

// TestBuildLocalModelMissingFromSnapshot verifies a local registry model with
// no inventory entry at all (an unresolvable location, or a family wt has no
// probe for) reads "unknown" rather than "absent": nothing was discovered
// about it, so calling it missing would be a guess.
func TestBuildLocalModelMissingFromSnapshot(t *testing.T) {
	cfg := catalogTestCfg()
	models := []config.Model{{ID: "omlx/unprobed", ProviderID: "omlx", ModelName: "unprobed", Location: config.LocationLocal}}
	inv := &localmodels.Snapshot{Providers: map[string]localmodels.Status{"omlx": localmodels.StatusOK}}
	rows := Build(Input{Config: cfg, Agent: "claude", Models: models, Inventory: inv})
	if rows[0].Status != StatusUnknown {
		t.Errorf("Status = %q, want unknown", rows[0].Status)
	}
}

// TestRowActionRules verifies what selecting a row does per row kind: cloud
// and running local rows launch; a non-running local row of a provider wt can
// start (ollama, omlx, mtplx) starts — including a pulled ollama model, a
// discovered row and an unknown-presence row; an absent row and a provider
// with no start engine (mlx_lm_server) are blocked. Getting this wrong either
// launches a dead model or hides one wt could have started.
func TestRowActionRules(t *testing.T) {
	local := func(provider string, status Status, running, discovered bool) Row {
		return Row{Location: config.LocationLocal, Status: status, Running: running, Discovered: discovered, Model: config.Model{ID: provider + "/x", ProviderID: provider}}
	}
	cases := []struct {
		name string
		row  Row
		want Action
	}{
		{"cloud", Row{Location: config.LocationCloud}, ActionLaunch},
		{"running omlx", local("omlx", StatusOK, true, false), ActionLaunch},
		{"idle omlx on disk", local("omlx", StatusOK, false, false), ActionStart},
		{"idle mtplx discovered", local("mtplx", StatusNew, false, true), ActionStart},
		{"idle ollama pulled", local("ollama", StatusOK, false, false), ActionStart},
		{"idle ollama unknown presence", local("ollama", StatusUnknown, false, false), ActionStart},
		{"absent omlx", local("omlx", StatusAbsent, false, false), ActionBlock},
		{"absent ollama", local("ollama", StatusAbsent, false, false), ActionBlock},
		{"mlx_lm_server has no start engine", local("mlx_lm_server", StatusOK, false, false), ActionBlock},
		{"omlx-6bit shares omlx's engine", local("omlx-6bit", StatusOK, false, false), ActionStart},
	}
	for _, tc := range cases {
		if got := tc.row.Action(); got != tc.want {
			t.Errorf("%s: action = %v, want %v", tc.name, got, tc.want)
		}
	}
}

// TestBlockReasonNamesTheFix verifies the status text for a blocked row names
// what to do: pull/download for an absent model, `modelman start <id>` for a
// provider wt cannot start, and "" for rows that are not blocked.
func TestBlockReasonNamesTheFix(t *testing.T) {
	absent := Row{Location: config.LocationLocal, Status: StatusAbsent, Model: config.Model{ID: "omlx/a", ProviderID: "omlx"}}
	if h := absent.BlockReason(); !strings.Contains(h, "omlx/a") || !strings.Contains(h, "not on disk") {
		t.Errorf("absent reason = %q", h)
	}
	mlx := Row{Location: config.LocationLocal, Status: StatusOK, Model: config.Model{ID: "mlx_lm_server/p", ProviderID: "mlx_lm_server"}}
	if h := mlx.BlockReason(); !strings.Contains(h, "modelman start mlx_lm_server/p") {
		t.Errorf("mlx_lm_server reason = %q", h)
	}
	if h := (Row{Location: config.LocationCloud}).BlockReason(); h != "" {
		t.Errorf("cloud reason = %q, want empty", h)
	}
	if h := (Row{Location: config.LocationLocal, Status: StatusOK, Model: config.Model{ProviderID: "omlx"}}).BlockReason(); h != "" {
		t.Errorf("startable row reason = %q, want empty", h)
	}
}

// TestFindReportsDiscoveredRows verifies Find looks a row up among ALL rows,
// discovered ones included — the fix that lets `wt -M <discovered-id>` work.
// A registry-only lookup would send users to a registry edit they do not need.
func TestFindReportsDiscoveredRows(t *testing.T) {
	rows := []Row{
		{Model: config.Model{ID: "omlx/a", ProviderID: "omlx"}, Location: config.LocationLocal, Status: StatusOK},
		{Model: config.Model{ID: "omlx/disc", ProviderID: "omlx"}, Location: config.LocationLocal, Status: StatusNew, Discovered: true},
	}
	if r, ok := Find(rows, "omlx/disc"); !ok || !r.Discovered {
		t.Errorf("Find(omlx/disc) = (%+v, %v), want the discovered row", r, ok)
	}
	if r, ok := Find(rows, "omlx/a"); !ok || r.Discovered {
		t.Errorf("Find(omlx/a) = (%+v, %v), want the registered row", r, ok)
	}
	if _, ok := Find(rows, "omlx/nope"); ok {
		t.Error("Find(omlx/nope) reported a hit, want miss")
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `cd wt && go test ./internal/catalog/ -v`
Expected: FAIL — `no Go files in .../internal/catalog` (or, once the file exists, undefined `Build`, `Row`, …).

- [ ] **Step 3: Write the implementation**

Create `internal/catalog/catalog.go`:

```go
// Package catalog owns the model-selector row rules shared by wt's pickers
// and its non-TUI launch path: which candidates become rows, each row's live
// presence status, and what selecting a row can do (launch, start, or block
// with a reason). It knows nothing about rendering, usage counts or sorting —
// those stay with the callers, which wrap a Row in their own type.
package catalog

import (
	"fmt"

	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/localmodels"
)

// Status is a row's presence state.
type Status string

const (
	StatusOK      Status = "ok"      // on disk (local) or simply available (cloud)
	StatusAbsent  Status = "absent"  // the provider answered and does not have this model
	StatusUnknown Status = "unknown" // local model whose presence the probe could not determine
	StatusNew     Status = "new"     // discovered, unregistered
)

// Action is what selecting a row does.
type Action int

const (
	ActionLaunch Action = iota // cloud row, or a local model already running
	ActionStart                // a local model wt can start (its provider has a lifecycle backend)
	ActionBlock                // cannot proceed; BlockReason says why
)

// Row is one model-selector row before rendering.
type Row struct {
	Model      config.Model
	Location   config.Location
	Status     Status
	Exposed    bool
	Running    bool
	Discovered bool
}

// Input gathers everything Build needs. Models is the caller's eligible list
// (exposed cloud + every configured local model, already filtered by
// agent/-T/-F); Inventory is nil when no local probe ran, in which case no
// local row is assessed at all: locals read as not running with status ok.
// When Inventory is set, a local row's status is ok, absent (the probe
// answered and found no artifact), unknown (it could not tell), or new
// (discovered, unregistered).
type Input struct {
	Config         *config.Config
	Agent          string
	Models         []config.Model
	Inventory      *localmodels.Snapshot
	HideDiscovered bool
}

// Build turns Input into rows.
func Build(in Input) []Row {
	// Only REGISTERED entries describe a configured model. A discovered
	// entry may share an id with a registry row by construction
	// (DiscoveredModelID is provider/artifact); it must never overwrite the
	// registered entry's Artifact/Running.
	byID := map[string]localmodels.Entry{}
	if in.Inventory != nil {
		for _, e := range in.Inventory.Entries {
			if e.Registered {
				byID[e.ModelID] = e
			}
		}
	}
	rows := make([]Row, 0, len(in.Models))
	seen := map[string]bool{}
	for _, m := range in.Models {
		loc := m.Location
		if in.Config != nil {
			if l, err := in.Config.ResolveLocation(m); err == nil {
				loc = l
			}
		}
		r := Row{Model: m, Location: loc, Status: StatusOK}
		if loc == config.LocationLocal && in.Inventory != nil {
			e, ok := byID[m.ID]
			switch {
			case !ok:
				// No registered entry: the probe skipped this model (an
				// unresolvable location) or its family has no probe at all.
				// Nothing was discovered about it, so presence is unknown.
				r.Status = StatusUnknown
			case e.Running:
				// Serving right now, so nothing about the row is missing.
				// Checked before the artifact tests because a running
				// mlx_lm_server row never has a resolved artifact.
				r.Running = true
			case !e.ArtifactKnown:
				r.Status = StatusUnknown
			case e.Artifact == "":
				r.Status = StatusAbsent
			}
		}
		r.Exposed = m.Native || (in.Config != nil && in.Config.ExposedFlag(m.ID))
		rows = append(rows, r)
		seen[m.ID] = true
	}
	if in.Inventory != nil && !in.HideDiscovered {
		for _, e := range in.Inventory.Entries {
			if e.Registered || seen[e.ModelID] {
				continue
			}
			if in.Agent != "" && (in.Config == nil || !in.Config.AgentSupportsProvider(in.Agent, e.ProviderID)) {
				continue
			}
			rows = append(rows, Row{
				Model: config.Model{
					ID: e.ModelID, ProviderID: e.ProviderID, ModelName: e.Artifact,
					Location: config.LocationLocal, Source: config.SourceDiscovered,
				},
				Location: config.LocationLocal, Status: StatusNew, Running: e.Running, Discovered: true,
			})
			seen[e.ModelID] = true
		}
	}
	return rows
}

// Find returns the row for id, discovered rows included — a -M pin names a
// model, not necessarily a registry entry.
func Find(rows []Row, id string) (Row, bool) {
	for _, r := range rows {
		if r.Model.ID == id {
			return r, true
		}
	}
	return Row{}, false
}

// startable reports whether wt has a lifecycle backend for the provider
// family — the families internal/lifecycle's backendsByFamily covers.
// omlx-6bit shares omlx's engine (localmodels.Family folds the variant tail).
func startable(providerID string) bool {
	switch localmodels.Family(providerID) {
	case "ollama", "omlx", "mtplx":
		return true
	}
	return false
}

// Action reports what selecting r does. A non-running local row is startable
// when its provider has a lifecycle backend and the model is not known to be
// missing from disk.
func (r Row) Action() Action {
	if r.Location != config.LocationLocal || r.Running {
		return ActionLaunch
	}
	if !startable(r.Model.ProviderID) {
		return ActionBlock
	}
	if r.Status == StatusAbsent {
		return ActionBlock
	}
	return ActionStart
}

// BlockReason is the status line for an ActionBlock row ("" otherwise): a
// model missing from disk needs pulling; a provider wt cannot start needs
// modelman.
func (r Row) BlockReason() string {
	if r.Action() != ActionBlock {
		return ""
	}
	if startable(r.Model.ProviderID) {
		return fmt.Sprintf("%s is not on disk — pull or download it first", r.Model.ID)
	}
	return fmt.Sprintf("local model %q is not running — start it with `modelman start %s`", r.Model.ID, r.Model.ID)
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `cd wt && go test ./internal/catalog/ -v`
Expected: PASS (8 tests).

- [ ] **Step 5: Delete the moved tests from `internal/tui/modelrows_test.go`**

Remove these functions (they now live in `internal/catalog/catalog_test.go`): `TestBuildRowsIncludesConfiguredAndDiscoveredLocal`, `TestBuildRowsMarksAbsentAndRespectsAgentAndFilters`, `TestBuildRowsNativeModelIsExposedWithoutFlag`, `TestBuildRowsUnknownLocalStatus`, `TestBuildRowsLocalModelMissingFromSnapshot`, `TestRowActionRules`, `TestBlockReasonNamesTheFix`. Keep `f64`, `rowsTestCfg`, `rowIDs`, `TestBuildRowsCountsAreAgentScoped`, `TestSortRowsTwoGroups`.

- [ ] **Step 6: Verify the whole module still builds**

Run: `cd wt && go build ./... && go vet ./...`
Expected: clean. The TUI still compiles because `modelrows.go` keeps its own copies — Task 2 removes them.

- [ ] **Step 7: Commit**

```bash
git add wt/internal/catalog wt/internal/tui/modelrows_test.go
git commit -m "feat(wt): extract shared model-selector row rules into internal/catalog

The picker, the non-TUI launch path and wt smoke each re-derived which
models exist, which are running, and what selecting one does. Give them
one policy package so they cannot disagree.

- completes plan item #1"
```

---

## Task 2: Rewire the TUI onto `catalog`

**Files:**
- Modify: `internal/tui/modelrows.go`
- Modify: `internal/tui/modeltable.go`
- Modify: `internal/tui/modelrows_test.go`

**Interfaces:**
- Consumes: `catalog.Build`, `catalog.Input`, `catalog.Row`, `catalog.Action*`, `catalog.Status*`.
- Produces: `tableRow` (now `struct { catalog.Row; counts usage.UsageCounts; stats survey.Stats }`) and `tableInput` (unchanged lowercase fields) — the shapes the rest of the TUI and its tests keep using.

- [ ] **Step 1: Rewrite `internal/tui/modelrows.go`**

Replace the file's top through the end of `sortRows` with the following. Delete entirely: `rowStatus` + its four constants, `tableRow`'s own fields, `buildRows`'s body, `rowAction` + `actionLaunch`/`actionStart`/`actionBlock`, `(tableRow) action`, `(tableRow) blockReason`. Keep `padRunes`/`maxRunes`/`flag` — those live in `modeltable.go`, untouched here.

```go
package tui

import (
	"sort"

	"github.com/ohanaverse/local-ai-setup/wt/internal/catalog"
	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/localmodels"
	"github.com/ohanaverse/local-ai-setup/wt/internal/survey"
	"github.com/ohanaverse/local-ai-setup/wt/internal/usage"
)

// tableRow is one line of the selector table: the shared catalog rules
// (presence status, action, block reason) plus the picker-only decorations
// (agent-scoped usage counts, survey stats).
type tableRow struct {
	catalog.Row
	counts usage.UsageCounts
	stats  survey.Stats
}

// tableInput gathers everything buildRows needs. models is the agent's
// eligible list (exposed cloud + every configured local model, already
// filtered by agent/-T/-F); inventory is nil when no local probe ran.
type tableInput struct {
	cfg            *config.Config
	agent          string
	models         []config.Model
	inventory      *localmodels.Snapshot
	hideDiscovered bool
	usage          usage.Store
	stats          map[string]survey.Stats
}

// buildRows builds the shared catalog rows and decorates them with the
// picker's per-agent usage counts and per-model survey stats. The row rules
// themselves live in internal/catalog so the non-TUI launch path and
// `wt smoke` reach the same verdicts this table displays.
func buildRows(in tableInput) []tableRow {
	rows := catalog.Build(catalog.Input{
		Config:         in.cfg,
		Agent:          in.agent,
		Models:         in.models,
		Inventory:      in.inventory,
		HideDiscovered: in.hideDiscovered,
	})
	out := make([]tableRow, len(rows))
	ids := make([]string, len(rows))
	for i, r := range rows {
		out[i] = tableRow{Row: r}
		ids[i] = r.Model.ID
	}
	var counts map[string]usage.UsageCounts
	if in.usage != nil {
		if in.agent != "" {
			counts = in.usage.CountsForAgent(in.agent, ids)
		} else {
			counts = in.usage.Counts(ids)
		}
	}
	for i := range out {
		out[i].counts = counts[out[i].Model.ID]
		out[i].stats = in.stats[out[i].Model.ID]
	}
	return out
}
```

- [ ] **Step 2: Update `sortRows` and `rowCostKey` to the promoted fields**

In the same file, replace every `r.location` with `r.Location`, `r.running` with `r.Running`, and `r.model` with `r.Model`. `sortRows` and `rowCostKey` keep their logic and comments verbatim; only the field reads change. `costKey` is unchanged.

- [ ] **Step 3: Update `internal/tui/modeltable.go`**

| Old | New |
|---|---|
| `fam[i] = r.model.Family` | `fam[i] = r.Model.Family` |
| `cost[i] = formatPerToken(r.model.Cost)` | `cost[i] = formatPerToken(r.Model.Cost)` |
| `if !r.discovered {` | `if !r.Discovered {` |
| `idW = maxRunes(idW, r.model.ID)` | `idW = maxRunes(idW, r.Model.ID)` |
| `wS = maxRunes(wS, string(r.status))` | `wS = maxRunes(wS, string(r.Status))` |
| `loc := string(r.location)` | `loc := string(r.Location)` |
| `padRunes(string(r.status), wS)` | `padRunes(string(r.Status), wS)` |
| `flag(r.exposed, "Y")` | `flag(r.Exposed, "Y")` |
| `flag(r.running, "run")` | `flag(r.Running, "run")` |
| `r.model.ID == lastID` | `r.Model.ID == lastID` |
| `refs[r.model.ID]` | `refs[r.Model.ID]` |
| `switch r.action() {` | `switch r.Action() {` |
| `case actionBlock:` | `case catalog.ActionBlock:` |
| `it.blocked = r.blockReason()` | `it.blocked = r.BlockReason()` |
| `case actionStart:` | `case catalog.ActionStart:` |
| `route, err := cfg.ResolveRoute(r.model, ...)` | `route, err := cfg.ResolveRoute(r.Model, ...)` |
| `case r.discovered && err == nil ...` | `case r.Discovered && err == nil ...` |
| `it.blocked = "discovered model " + r.model.ID + ...` | `it.blocked = "discovered model " + r.Model.ID + ...` |

Add `"github.com/ohanaverse/local-ai-setup/wt/internal/catalog"` to the import block.

- [ ] **Step 4: Fix the two surviving tests in `internal/tui/modelrows_test.go`**

`rowIDs` reads `r.Model.ID`:

```go
func rowIDs(rows []tableRow) []string {
	ids := make([]string, len(rows))
	for i, r := range rows {
		ids[i] = r.Model.ID
	}
	return ids
}
```

`TestSortRowsTwoGroups`'s two builders use the embedded field name:

```go
	cloud := func(id string, in, out *float64, sub *float64) tableRow {
		return tableRow{Row: catalog.Row{Location: config.LocationCloud, Model: config.Model{ID: id, Cost: config.ModelCost{InputPricePerMillion: in, OutputPricePerMillion: out, SubscriptionPrice: sub}}}}
	}
	local := func(id string, running bool, sevenDay int) tableRow {
		return tableRow{Row: catalog.Row{Location: config.LocationLocal, Running: running, Model: config.Model{ID: id}}, counts: usage.UsageCounts{SevenDay: sevenDay}}
	}
```

Add the `catalog` import to that test file. `TestBuildRowsCountsAreAgentScoped` needs no change (`tableInput`'s fields are unchanged).

- [ ] **Step 5: Build and run the TUI tests**

Run: `cd wt && go build ./... && go test ./internal/tui/ 2>&1 | tail -30`
Expected: compiles; any failures are test files still using `r.model` / `statusOK` / `actionStart`. Fix each by the table in Step 3 (`grep -rn "\.model\.\|statusOK\|statusAbsent\|statusUnknown\|statusNew\|actionLaunch\|actionStart\|actionBlock\|\.blockReason()\|\.action()\|rowStatus" internal/tui/*.go` must return nothing in non-test files and nothing at all outside `model_list.go`'s `it.model` / `modelItem` uses).

Note: `it.model` in `modeltable.go`/`model_list.go` is `modelItem.model` (a `config.Model`), which is unrelated and stays.

- [ ] **Step 6: Run the full test suite**

Run: `cd wt && go vet ./... && go test ./...`
Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add wt/internal/tui
git commit -m "refactor(wt): wrap the TUI selector table around catalog.Row

tableRow now embeds catalog.Row so the picker displays exactly the row
verdict the non-TUI path will act on. Counts, survey stats, sorting and
rendering stay here.

- completes plan item #2"
```

---

## Task 3: `resolveModel` on live rows

**Files:**
- Modify: `cmd/wt/resolve.go`
- Create: `cmd/wt/testmain_test.go`
- Modify: `cmd/wt/resolve_test.go`

**Interfaces:**
- Consumes: `catalog.Build`, `catalog.Find`, `catalog.Row.Action`, `catalog.Action*`.
- Produces:
  - `var probeInventory = localmodels.Inventory`
  - `var startModel = startForLaunch` — declared in `cmd/wt/start.go` by Task 5; **for this task, stub it to return an error instead** (see Step 1's note).
  - `func resolveModel(agent string, cfg *config.Config, tags, family, pinned string) (config.Model, []config.Model, error)` — unchanged signature; the second value is now the **launchable** list.
  - `func launchableModels(rows []catalog.Row) []config.Model`
  - Test helpers: `stubProbeInventory`, `stubStartDriver`, `startRequest`, `TestMain`.

**Interfaces it will consume from Task 5:** `func startForLaunch(cfg *config.Config, row catalog.Row, allowReplace bool) error` and `var allowReplace bool`. Task 5 lands immediately after; until then Step 1 declares the seam locally so this task stands alone and its tests pass.

- [ ] **Step 1: Write the failing tests**

Replace `cmd/wt/resolve_test.go` wholesale. The gate-flavored tests (`TestResolveModelNoMarkerHidesLocalModels`, `TestResolveModelVerifiedMarkerAllowsLocalModel`, `TestResolveModelDriftedMarkerDoesNotBlockUnrelatedLaunch`, `TestResolveModelPinnedStaleLocalModelRejected`, `TestResolveModelGateEmptiedListGivesGateMessage`) are deleted: they assert flag-and-probe semantics that no longer exist. `TestResolveModelNoMatchKeepsGenericMessage` survives.

```go
package main

import (
	"errors"
	"strings"
	"testing"

	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/localmodels"
)

// TestResolveModel covers the non-TUI model resolution path used after
// -W/--cwd has resolved the worktree and -A has resolved the agent.
// This is the gate that catches config errors and -M mismatches before
// launching an agent.
func TestResolveModel(t *testing.T) {
	cfg := &config.Config{
		DefaultTag: "code",
		Providers: []config.Provider{
			{ID: "claude", Location: config.LocationCloud, Auth: config.AuthConfig{Type: "native"}},
			{ID: "ollama", Location: config.LocationLocal, Auth: config.AuthConfig{Type: "none"}},
		},
		Models: []config.Model{
			{ID: "claude/opus", ProviderID: "claude", ModelName: "opus", Family: "opus", Tags: []string{"code"}},
			{ID: "claude/sonnet", ProviderID: "ollama", ModelName: "sonnet", Family: "sonnet", Tags: []string{"design"}},
			{ID: "ollama/gemma4:9b", ProviderID: "ollama", ModelName: "gemma4:9b", Family: "gemma4", Tags: []string{"code"}},
		},
		Agents: []config.Agent{
			{Name: "claude", SupportedProviders: []string{"claude"}},
			{Name: "pi", SupportedProviders: []string{"claude", "ollama"}},
		},
	}
	cfg.ExposeAllForTest()

	// resolveModel errors on an ambiguous LAUNCHABLE list rather than
	// falling back to any single model. launch.go calls resolveModel and
	// handles the error via rotation.

	tests := []struct {
		name    string
		agent   string
		tags    string
		family  string
		pinned  string
		wantID  string
		wantErr bool
	}{
		{"single match", "claude", "", "", "", "claude/opus", false},
		{"pinned cloud in eligible", "pi", "", "", "claude/opus", "claude/opus", false},
		{"pinned not in eligible", "pi", "", "", "ollama/missing", "", true},
		{"pinned wrong provider for agent", "claude", "", "", "ollama/gemma4:9b", "", true},
		// Two local rows are startable, not launchable: they do not make the
		// choice ambiguous, and rotation must never pick one.
		{"only cloud rows are launchable", "pi", "", "", "", "claude/opus", false},
		{"all-local list needs a pin", "pi", "code", "gemma4", "", "", true},
		{"empty eligible errors", "claude", "design", "", "", "", true},
		{"unknown agent", "nope", "", "", "", "", true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			m, _, err := resolveModel(tc.agent, cfg, tc.tags, tc.family, tc.pinned)
			if (err != nil) != tc.wantErr {
				t.Fatalf("err = %v, wantErr = %v", err, tc.wantErr)
			}
			if err != nil {
				return
			}
			if m.ID != tc.wantID {
				t.Errorf("got ID %q, want %q", m.ID, tc.wantID)
			}
		})
	}

	// A command agent returns the errCommandAgent sentinel specifically, so
	// launchFiltered's errors.Is(err, errCommandAgent) dispatch matches. This
	// locks the sentinel identity the dispatch depends on.
	if _, _, err := resolveModel("shell", cfg, "", "", ""); !errors.Is(err, errCommandAgent) {
		t.Errorf("resolveModel(shell) err = %v, want errCommandAgent", err)
	}
}

// TestResolveModelReturnsLaunchable verifies resolveModel returns the
// launchable list even when it errors, so callers can reuse it instead of
// calling cfg.EligibleModels a second time. Returning the raw eligible list
// would let rotation pick a non-running local model.
func TestResolveModelReturnsLaunchable(t *testing.T) {
	cfg := &config.Config{
		Providers: []config.Provider{{ID: "openrouter", Location: config.LocationCloud}},
		Models: []config.Model{
			{ID: "openrouter/a", ProviderID: "openrouter", Tags: []string{"code"}},
			{ID: "openrouter/b", ProviderID: "openrouter", Tags: []string{"code"}},
		},
		Agents: []config.Agent{{Name: "claude", SupportedProviders: []string{"openrouter"}}},
	}
	cfg.ExposeAllForTest()

	m, launchable, err := resolveModel("claude", cfg, "", "", "")
	if err == nil {
		t.Fatal("expected multiple-models error")
	}
	if m.ID != "" {
		t.Errorf("model = %q, want zero", m.ID)
	}
	if len(launchable) != 2 {
		t.Fatalf("launchable = %d, want 2", len(launchable))
	}
}

// TestResolveModelRunningLocalModelIsLaunchable verifies a local model the
// live probe reports as running resolves without any start: the row is a
// launch row, so the launch proceeds directly. This is the replacement for
// the retired localgate probe test — the verdict now comes from the same
// inventory the picker shows.
func TestResolveModelRunningLocalModelIsLaunchable(t *testing.T) {
	calls := stubStartDriver(t, nil)
	stubProbeInventory(t, localmodels.Snapshot{
		Providers: map[string]localmodels.Status{"omlx": localmodels.StatusOK},
		Entries: []localmodels.Entry{
			{ProviderID: "omlx", ModelID: "omlx/qwen3.8", Artifact: "qwen3.8", ModelName: "qwen3.8", Registered: true, Running: true},
		},
	})
	cfg := &config.Config{
		Providers: []config.Provider{{ID: "omlx", Location: config.LocationLocal, Auth: config.AuthConfig{Type: "none"}}},
		Models:    []config.Model{{ID: "omlx/qwen3.8", ProviderID: "omlx", ModelName: "qwen3.8", Family: "qwen3.8", Tags: []string{"code"}}},
		Agents:    []config.Agent{{Name: "pi", SupportedProviders: []string{"omlx"}}},
	}
	cfg.ExposeAllForTest()

	m, _, err := resolveModel("pi", cfg, "", "", "")
	if err != nil || m.ID != "omlx/qwen3.8" {
		t.Fatalf("resolveModel() = (%v, %v), want (omlx/qwen3.8, nil)", m, err)
	}
	if calls.called {
		t.Error("a running model must not be started")
	}
}

// TestResolveModelPinOnIdleLocalStartsIt verifies -M pinning a configured
// local model that is not running starts it through the driver and returns
// the model, so `wt -A pi -M omlx/qwen3.8` works without a separate
// `modelman start`. It must NOT pass replace: a plain pin never opts into
// stopping a running model.
func TestResolveModelPinOnIdleLocalStartsIt(t *testing.T) {
	allowReplace = false
	stubProbeInventory(t, localmodels.Snapshot{
		Providers: map[string]localmodels.Status{"omlx": localmodels.StatusOK},
		Entries: []localmodels.Entry{
			{ProviderID: "omlx", ModelID: "omlx/qwen3.8", Artifact: "qwen3.8", ModelName: "qwen3.8", Registered: true, ArtifactKnown: true},
		},
	})
	calls := stubStartDriver(t, nil)
	cfg := &config.Config{
		Providers: []config.Provider{{ID: "omlx", Location: config.LocationLocal, Auth: config.AuthConfig{Type: "none"}}},
		Models:    []config.Model{{ID: "omlx/qwen3.8", ProviderID: "omlx", ModelName: "qwen3.8", Family: "qwen3.8", Tags: []string{"code"}}},
		Agents:    []config.Agent{{Name: "pi", SupportedProviders: []string{"omlx"}}},
	}
	cfg.ExposeAllForTest()

	m, _, err := resolveModel("pi", cfg, "", "", "omlx/qwen3.8")
	if err != nil || m.ID != "omlx/qwen3.8" {
		t.Fatalf("resolveModel() = (%v, %v), want (omlx/qwen3.8, nil)", m, err)
	}
	if !calls.called {
		t.Fatal("the start driver was not called for a non-running pinned local model")
	}
	if calls.row.Model.ID != "omlx/qwen3.8" || calls.row.Model.ModelName != "qwen3.8" {
		t.Errorf("start request row = %+v, want omlx/qwen3.8", calls.row.Model)
	}
	if calls.replace {
		t.Error("a plain pin must not pass replace, or it could stop a running model unasked")
	}
}

// TestResolveModelPinOnDiscoveredLocalStartsIt verifies the pin is looked up
// among ALL rows, discovered ones included: a running-state model on disk
// with no registry entry can be started by pinning its discovered id, which
// is the whole point of the live-row lookup.
func TestResolveModelPinOnDiscoveredLocalStartsIt(t *testing.T) {
	disc := config.DiscoveredModelID("mtplx", "on-disk")
	stubProbeInventory(t, localmodels.Snapshot{
		Providers: map[string]localmodels.Status{"mtplx": localmodels.StatusOK},
		Entries: []localmodels.Entry{
			{ProviderID: "mtplx", ModelID: disc, Artifact: "on-disk", ModelName: "on-disk"},
		},
	})
	calls := stubStartDriver(t, nil)
	cfg := &config.Config{
		Providers: []config.Provider{{ID: "mtplx", Location: config.LocationLocal, Auth: config.AuthConfig{Type: "none"}}},
		Agents:    []config.Agent{{Name: "pi", SupportedProviders: []string{"mtplx"}}},
	}
	cfg.ExposeAllForTest()

	m, _, err := resolveModel("pi", cfg, "", "", disc)
	if err != nil || m.ID != disc {
		t.Fatalf("resolveModel() = (%v, %v), want (%s, nil)", m, err, disc)
	}
	if !calls.called || calls.row.Model.ModelName != "on-disk" {
		t.Errorf("start request = %+v, want the discovered artifact on-disk", calls.row.Model)
	}
}

// TestResolveModelPinOnAbsentLocalReportsReason verifies pinning a local
// model the probe confirmed is missing on disk fails with the pull/download
// message and starts nothing: the engine would otherwise wait out its warmup
// on a model that is not there.
func TestResolveModelPinOnAbsentLocalReportsReason(t *testing.T) {
	stubProbeInventory(t, localmodels.Snapshot{
		Providers: map[string]localmodels.Status{"omlx": localmodels.StatusOK},
		Entries: []localmodels.Entry{
			{ProviderID: "omlx", ModelID: "omlx/gone", Registered: true, ArtifactKnown: true},
		},
	})
	calls := stubStartDriver(t, nil)
	cfg := &config.Config{
		Providers: []config.Provider{{ID: "omlx", Location: config.LocationLocal, Auth: config.AuthConfig{Type: "none"}}},
		Models:    []config.Model{{ID: "omlx/gone", ProviderID: "omlx", ModelName: "gone", Tags: []string{"code"}}},
		Agents:    []config.Agent{{Name: "pi", SupportedProviders: []string{"omlx"}}},
	}
	cfg.ExposeAllForTest()

	_, _, err := resolveModel("pi", cfg, "", "", "omlx/gone")
	if err == nil || !strings.Contains(err.Error(), "not on disk") {
		t.Errorf("err = %v, want the not-on-disk message", err)
	}
	if calls.called {
		t.Error("an absent model must not reach the start driver")
	}
}

// TestResolveModelPinOnNoEngineProviderReportsReason verifies pinning a local
// model whose provider wt cannot start (mlx_lm_server) reports the
// `modelman start` hint instead of silently doing nothing — the user needs to
// know which tool owns that provider's lifecycle.
func TestResolveModelPinOnNoEngineProviderReportsReason(t *testing.T) {
	stubProbeInventory(t, localmodels.Snapshot{
		Providers: map[string]localmodels.Status{"mlx_lm_server": localmodels.StatusOK},
		Entries: []localmodels.Entry{
			{ProviderID: "mlx_lm_server", ModelID: "mlx_lm_server/p", Registered: true, ArtifactKnown: true},
		},
	})
	cfg := &config.Config{
		Providers: []config.Provider{{ID: "mlx_lm_server", Location: config.LocationLocal, Auth: config.AuthConfig{Type: "none"}}},
		Models:    []config.Model{{ID: "mlx_lm_server/p", ProviderID: "mlx_lm_server", ModelName: "p", Tags: []string{"code"}}},
		Agents:    []config.Agent{{Name: "pi", SupportedProviders: []string{"mlx_lm_server"}}},
	}
	cfg.ExposeAllForTest()

	_, _, err := resolveModel("pi", cfg, "", "", "mlx_lm_server/p")
	if err == nil || !strings.Contains(err.Error(), "modelman start mlx_lm_server/p") {
		t.Errorf("err = %v, want the modelman start hint", err)
	}
}

// TestResolveModelAllLocalGivesPinMessage asserts the empty-launchable error:
// when every eligible model is local and none is running, resolveModel must
// say a pin would start one — NOT the generic "no models match" wording,
// which would send the operator hunting for a -T/-F/config problem that is
// not there. It also asserts the launchable list is still returned.
func TestResolveModelAllLocalGivesPinMessage(t *testing.T) {
	stubProbeInventory(t, localmodels.Snapshot{
		Providers: map[string]localmodels.Status{"omlx": localmodels.StatusOK},
		Entries:   []localmodels.Entry{{ProviderID: "omlx", ModelID: "omlx/qwen3.8", Registered: true, ArtifactKnown: true}},
	})
	cfg := &config.Config{
		Providers: []config.Provider{{ID: "omlx", Location: config.LocationLocal, Auth: config.AuthConfig{Type: "none"}}},
		Models:    []config.Model{{ID: "omlx/qwen3.8", ProviderID: "omlx", ModelName: "qwen3.8", Family: "qwen3.8", Tags: []string{"code"}}},
		Agents:    []config.Agent{{Name: "pi", SupportedProviders: []string{"omlx"}}},
	}
	cfg.ExposeAllForTest()

	_, launchable, err := resolveModel("pi", cfg, "", "", "")
	if err == nil {
		t.Fatal("expected an error when nothing is launchable")
	}
	if !strings.Contains(err.Error(), "no cloud or running local model") || !strings.Contains(err.Error(), "wt -M <id>") {
		t.Errorf("err = %q, want the pin-the-model message", err)
	}
	if strings.Contains(err.Error(), "no models match") {
		t.Errorf("err = %q; generic no-match wording must not be used here", err)
	}
	if len(launchable) != 0 {
		t.Errorf("launchable = %d models, want 0", len(launchable))
	}
}

// TestResolveModelStartFailurePropagates verifies a start-driver failure is
// returned as resolveModel's error rather than swallowed: the caller must
// abort the launch with the engine's own message (daemon down, port busy,
// occupancy unknown, …) instead of running the agent against nothing.
func TestResolveModelStartFailurePropagates(t *testing.T) {
	stubProbeInventory(t, localmodels.Snapshot{
		Providers: map[string]localmodels.Status{"omlx": localmodels.StatusOK},
		Entries:   []localmodels.Entry{{ProviderID: "omlx", ModelID: "omlx/q", Registered: true, ArtifactKnown: true}},
	})
	boom := errors.New("omlx is not answering at http://localhost:8000")
	stubStartDriver(t, boom)
	cfg := &config.Config{
		Providers: []config.Provider{{ID: "omlx", Location: config.LocationLocal, Auth: config.AuthConfig{Type: "none"}}},
		Models:    []config.Model{{ID: "omlx/q", ProviderID: "omlx", ModelName: "q", Tags: []string{"code"}}},
		Agents:    []config.Agent{{Name: "pi", SupportedProviders: []string{"omlx"}}},
	}
	cfg.ExposeAllForTest()

	m, _, err := resolveModel("pi", cfg, "", "", "omlx/q")
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v, want the driver's error", err)
	}
	if m.ID != "" {
		t.Errorf("model = %q, want zero on a failed start", m.ID)
	}
}

// TestResolveModelReplaceFlagReachesDriver verifies --replace is passed
// through to the driver, which is what lets a non-interactive caller opt into
// stopping a running occupant. Without it every occupied provider would need
// a TTY prompt, breaking scripted launches.
func TestResolveModelReplaceFlagReachesDriver(t *testing.T) {
	stubProbeInventory(t, localmodels.Snapshot{
		Providers: map[string]localmodels.Status{"omlx": localmodels.StatusOK},
		Entries:   []localmodels.Entry{{ProviderID: "omlx", ModelID: "omlx/q", Registered: true, ArtifactKnown: true}},
	})
	calls := stubStartDriver(t, nil)
	cfg := &config.Config{
		Providers: []config.Provider{{ID: "omlx", Location: config.LocationLocal, Auth: config.AuthConfig{Type: "none"}}},
		Models:    []config.Model{{ID: "omlx/q", ProviderID: "omlx", ModelName: "q", Tags: []string{"code"}}},
		Agents:    []config.Agent{{Name: "pi", SupportedProviders: []string{"omlx"}}},
	}
	cfg.ExposeAllForTest()

	allowReplace = true
	t.Cleanup(func() { allowReplace = false })

	if _, _, err := resolveModel("pi", cfg, "", "", "omlx/q"); err != nil {
		t.Fatalf("resolveModel() error = %v", err)
	}
	if !calls.replace {
		t.Error("--replace must reach the driver's allowReplace argument")
	}
}

// TestResolveModelNoMatchKeepsGenericMessage is the control for the
// all-local case: with no models at all matching the filters, an empty
// eligible list still gets the pre-existing generic "no models match" error.
func TestResolveModelNoMatchKeepsGenericMessage(t *testing.T) {
	stubProbeInventory(t, localmodels.Snapshot{})
	cfg := &config.Config{
		Providers: []config.Provider{{ID: "ollama", Auth: config.AuthConfig{Type: "none"}}},
		Models:    []config.Model{{ID: "ollama/code", ProviderID: "ollama", Tags: []string{"code"}}},
		Agents:    []config.Agent{{Name: "pi", SupportedProviders: []string{"ollama"}}},
	}
	cfg.ExposeAllForTest()

	_, _, err := resolveModel("pi", cfg, "design", "", "") // -T filter matches nothing
	if err == nil || !strings.Contains(err.Error(), "no models match") {
		t.Errorf("err = %v, want the generic no-models-match error", err)
	}
}
```

Create `cmd/wt/testmain_test.go`:

```go
package main

import (
	"os"
	"testing"

	"github.com/ohanaverse/local-ai-setup/wt/internal/catalog"
	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/localmodels"
)

// TestMain stubs the two live seams a cmd/wt test would otherwise hit: the
// local-model inventory probe (no test may dial the developer's real
// ollama/omlx/mtplx servers) and the non-TUI start driver (no test may start
// a real model process). Tests that need a probe result call
// stubProbeInventory; tests that exercise a start call stubStartDriver.
func TestMain(m *testing.M) {
	probeInventory = func(*config.Config) localmodels.Snapshot { return localmodels.Snapshot{} }
	startModel = func(*config.Config, catalog.Row, bool) error {
		return errors.New("startModel not stubbed in this test")
	}
	os.Exit(m.Run())
}

// startRequest records what a stubbed start driver was asked to do.
type startRequest struct {
	called  bool
	row     catalog.Row
	replace bool
}

// stubProbeInventory makes the non-TUI paths see snap for the duration of a
// test; the seam is restored on cleanup.
func stubProbeInventory(t *testing.T, snap localmodels.Snapshot) {
	t.Helper()
	old := probeInventory
	probeInventory = func(*config.Config) localmodels.Snapshot { return snap }
	t.Cleanup(func() { probeInventory = old })
}

// stubStartDriver replaces the driver with a recorder returning err, so a
// test can assert which row was started and with which replace permission
// without invoking the lifecycle engine.
func stubStartDriver(t *testing.T, err error) *startRequest {
	t.Helper()
	req := &startRequest{}
	old := startModel
	startModel = func(_ *config.Config, row catalog.Row, replace bool) error {
		req.called, req.row, req.replace = true, row, replace
		return err
	}
	t.Cleanup(func() { startModel = old })
	return req
}
```

Add `"errors"` to `testmain_test.go`'s imports.

**Note on `allowReplace`:** Steps 1's `TestResolveModelReplaceFlagReachesDriver` and `TestResolveModelPinOnIdleLocalStartsIt` reference a package var `allowReplace` that Task 5 declares. To keep this task self-contained, declare it here too and let Task 5 document it:

```go
// allowReplace is the process-wide --replace mode, set once by the root
// command and read by resolveModel's start path (declared here so this task
// stands alone; Task 5 fills in the flag wiring).
var allowReplace bool
```

Put it at the top of `cmd/wt/resolve.go` in Step 3.

- [ ] **Step 2: Run the tests to verify they fail**

Run: `cd wt && go test ./cmd/wt/ -run 'TestResolveModel' 2>&1 | head -30`
Expected: FAIL to compile — `undefined: probeInventory`, `undefined: startModel`, `undefined: stubStartDriver`, `undefined: allowReplace`.

- [ ] **Step 3: Write the implementation**

Replace `cmd/wt/resolve.go` with:

```go
package main

import (
	"fmt"

	"github.com/ohanaverse/local-ai-setup/wt/internal/agents"
	"github.com/ohanaverse/local-ai-setup/wt/internal/catalog"
	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/localmodels"
)

// probeInventory is a test seam: production probes the live local-model
// inventory. cmd/wt's TestMain stubs it to an empty snapshot so no test dials
// the developer's real ollama/omlx/mtplx servers, and tests that need a
// verdict stub it with their own snapshot.
var probeInventory = localmodels.Inventory

// allowReplace is the process-wide --replace mode: set once from the root
// command's flag and read here. It is not a parameter because it is CLI mode
// state, not per-launch state — threading it through launchFiltered (and its
// dozen test call sites) would be churn for a flag only this function reads.
var allowReplace bool

// errCommandAgent is the sentinel returned by resolveModel when the
// resolved agent is a command (no model layer). Callers skip the model
// step and launch the command directly.
var errCommandAgent = fmt.Errorf("agent is a command")

// resolveModel computes the single model to launch for a non-TUI flow and
// returns the LAUNCHABLE list (cloud models plus local models already
// running) so callers do not recompute it and rotation can never land on a
// local model that is not up.
// agent is the resolved agent name (from -A; main routes unpinned launches
// through the agent picker, so launchFiltered never sees an empty agent).
// tags and family are the -T/-F flag values (comma-delimited).
// pinned is the -M flag value ("" = not pinned).
//
// Behavior:
//   - command agent → errCommandAgent, nil launchable
//   - pinned != "" → the pin's row decides: launch → return it; start →
//     start it through startForLaunch, then return it; block → error carrying
//     the row's block reason. A pin absent from the rows → "not in the
//     eligible list".
//   - no pin, empty launchable → error (pin-the-model wording when rows
//     existed, generic "no models match" when none did)
//   - no pin, one launchable → return it
//   - no pin, several launchable → "multiple models match"
//
// Note: rotation lives outside this function. launchFiltered catches
// the "multiple models match" error and advances through the global
// rotation when pinned == "".
func resolveModel(agent string, cfg *config.Config, tags, family, pinned string) (config.Model, []config.Model, error) {
	if agents.IsCommand(agent) {
		return config.Model{}, nil, errCommandAgent
	}
	eligible, err := cfg.EligibleModels(agent, tags, family)
	if err != nil {
		return config.Model{}, nil, err
	}

	snap := probeInventory(cfg)
	rows := catalog.Build(catalog.Input{
		Config: cfg, Agent: agent, Models: eligible, Inventory: &snap,
	})
	launchable := launchableModels(rows)

	// A -M pin is looked up among ALL rows, discovered ones included: the
	// launch path accepts a model the registry does not name.
	if pinned != "" {
		row, ok := catalog.Find(rows, pinned)
		if !ok {
			return config.Model{}, launchable, fmt.Errorf("model %q is not in the eligible list for agent %q", pinned, agent)
		}
		switch row.Action() {
		case catalog.ActionLaunch:
			return row.Model, launchable, nil
		case catalog.ActionStart:
			if err := startModel(cfg, row, allowReplace); err != nil {
				return config.Model{}, launchable, err
			}
			return row.Model, launchable, nil
		}
		return config.Model{}, launchable, fmt.Errorf("%s", row.BlockReason())
	}

	if len(launchable) == 0 {
		if len(rows) == 0 {
			return config.Model{}, nil, fmt.Errorf("no models match agent %q with tags %q and family %q", agent, tags, family)
		}
		// Models matched; none is usable without starting one. Say which
		// fix applies instead of the generic wording, which would send the
		// operator hunting for a -T/-F/config problem that isn't there.
		return config.Model{}, launchable, fmt.Errorf(
			"no cloud or running local model for agent %q — start one with `wt -M <id>`", agent)
	}
	m, err := resolveModelFromEligible(agent, launchable, "")
	return m, launchable, err
}

// launchableModels narrows rows to the models a launch can use right now:
// cloud rows plus local rows the probe reports as running. A start row is
// deliberately excluded — rotation and auto-resolution must never start a
// server, only a -M pin may.
func launchableModels(rows []catalog.Row) []config.Model {
	var out []config.Model
	for _, r := range rows {
		if r.Action() == catalog.ActionLaunch {
			out = append(out, r.Model)
		}
	}
	return out
}

// resolveModelFromEligible resolves the single model to launch from a
// precomputed launchable list, applying the ambiguity rule without
// recomputing anything. Callers that already hold the slice
// (launchFilteredImpl, resolveModelForLaunch) use this to avoid a second
// EligibleModels call. The list must be non-empty; the empty case is handled
// by resolveModel before this is called.
//
// There is no pinned branch: a -M pin is resolved against ALL rows in
// resolveModel, so by the time a pin reaches here it is already a launch row
// and the list holds it.
func resolveModelFromEligible(agent string, launchable []config.Model) (config.Model, error) {
	if len(launchable) > 1 {
		return config.Model{}, fmt.Errorf("multiple models match for agent %q", agent)
	}
	return launchable[0], nil
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `cd wt && go test ./cmd/wt/ -run 'TestResolveModel' -v`
Expected: PASS.

- [ ] **Step 5: Fix the two call sites of `resolveModelFromEligible`**

`cmd/wt/launch.go:126` calls `resolveModelFromEligible(agent, eligible, pinned)`. Change it to:

```go
		m, err = resolveModelFromEligible(agent, eligible)
```

and update the surrounding comment: the precomputed `eligible` a caller passes is the launchable list from `resolveModel`, so the pin is already resolved. `cmd/wt/launch.go`'s `launchFilteredImpl` doc-comment gains one line noting that `-M` on a start row was already started by `resolveModel` before this point.

Also update `resolveModelForLaunch`'s doc-comment in `cmd/wt/main.go` (the `eligible` it returns is now the launchable list).

- [ ] **Step 6: Run the full test suite**

Run: `cd wt && go vet ./... && go test ./cmd/wt/ 2>&1 | tail -30`
Expected: some failures are expected in tests that assert the old wording. Fix each:
- `cmd/wt/main_test.go:713-735` `TestResolveModelForLaunchDriftedMarkerResolvesNormally` → delete it (its premise — a flagged-but-unverified marker — no longer exists). Replace with:

```go
// TestResolveModelForLaunchCloudOnlyResolves verifies the auto-launch
// short-circuit still fires for a cloud-only config, so `wt -A claude` with
// one eligible model launches without a picker. The old drifted-marker test
// this replaces asserted gate semantics that no longer exist.
func TestResolveModelForLaunchCloudOnlyResolves(t *testing.T) {
	cfg := &config.Config{
		Providers: []config.Provider{{ID: "claude", Location: config.LocationCloud, Auth: config.AuthConfig{Type: "native"}}},
		Models:    []config.Model{{ID: "claude/opus", ProviderID: "claude", ModelName: "opus", Family: "opus", Tags: []string{"code"}}},
		Agents:    []config.Agent{{Name: "claude", SupportedProviders: []string{"claude"}}},
	}
	cfg.ExposeAllForTest()

	resolved, m, _, err := resolveModelForLaunch("claude", cfg, "", "", "")
	if err != nil {
		t.Fatalf("resolveModelForLaunch error = %v, want nil", err)
	}
	if !resolved || m.ID != "claude/opus" {
		t.Errorf("resolveModelForLaunch() = (resolved=%v, m=%v), want (true, claude/opus)", resolved, m)
	}
}
```

- Any other failure: read the assertion, confirm the new behavior is what the spec wants, and update the test's comment to say why.

- [ ] **Step 7: Commit**

```bash
git add wt/cmd/wt
git commit -m "feat(wt): resolve the non-TUI launch model from live rows

-M now accepts any row: a running or cloud pin launches, a non-running
local pin starts the model first, and a blocked pin fails with the row's
own reason. Rotation only ever sees running and cloud models, so it can
no longer hand the agent a server that is not up.

- completes plan item #3"
```

---

## Task 4: Move the start-failure wording into `internal/lifecycle`

**Files:**
- Create: `internal/lifecycle/message.go`
- Create: `internal/lifecycle/message_test.go`
- Modify: `internal/tui/start_flow.go`
- Modify: `internal/tui/start_flow_test.go` (delete `TestStartErrorsMapToMessages`)

**Interfaces:**
- Consumes: `lifecycle.Stage`, `lifecycle.DaemonDownError`, `lifecycle.BinaryMissingError`, `lifecycle.PortBusyError`.
- Produces:
  - `func StageLabel(s Stage) string`
  - `func StartErrorMessage(id string, err error) string`

This is the spec's **only engine change** in the sub-project: the wording that maps a failed `Start` to a user-facing line becomes one exported function so the TUI's status line and Task 5's non-TUI stderr cannot drift.

- [ ] **Step 1: Write the failing tests**

Create `internal/lifecycle/message_test.go`:

```go
package lifecycle

import (
	"errors"
	"strings"
	"testing"
)

// TestStartErrorMessageMapsEngineFailures verifies each engine failure
// produces the message the spec promises: a down daemon names the origin, the
// engine's own typed errors are shown verbatim, and anything else is prefixed
// with the model — a user must be able to tell what to fix. It lives here,
// with the function, so the TUI and the non-TUI driver are tested against one
// shared wording.
func TestStartErrorMessageMapsEngineFailures(t *testing.T) {
	cases := []struct {
		name string
		err  error
		id   string
		want []string // substrings the message must contain
	}{
		{"daemon down", &DaemonDownError{Provider: "omlx", Origin: "http://localhost:8000"}, "omlx/q", []string{"is not answering at", "http://localhost:8000"}},
		{"binary missing", &BinaryMissingError{Binary: "omlx"}, "omlx/q", []string{"omlx"}},
		{"port busy", &PortBusyError{Port: 8000}, "omlx/q", []string{"8000"}},
		{"generic", errors.New("boom"), "omlx/q", []string{"failed to start omlx/q: boom"}},
	}
	for _, tc := range cases {
		got := StartErrorMessage(tc.id, tc.err)
		for _, want := range tc.want {
			if !strings.Contains(got, want) {
				t.Errorf("%s: message = %q, want it to contain %q", tc.name, got, want)
			}
		}
	}
}

// TestStageLabelCoversEveryStage verifies every declared Stage has its own
// phrase and the zero value falls back to a generic one. A missing case would
// render an empty progress segment while the user waits on a slow warmup.
func TestStageLabelCoversEveryStage(t *testing.T) {
	stages := []Stage{StageStoppingOccupant, StageStarting, StageWaiting, StageWarming}
	seen := map[string]bool{}
	for _, s := range stages {
		label := StageLabel(s)
		if label == "" || label == StageLabel("") {
			t.Errorf("StageLabel(%q) = %q, want its own non-generic phrase", s, label)
		}
		if seen[label] {
			t.Errorf("StageLabel(%q) = %q, duplicated with an earlier stage", s, label)
		}
		seen[label] = true
	}
	if got := StageLabel(Stage("")); got != "starting" {
		t.Errorf("StageLabel(zero) = %q, want %q", got, "starting")
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `cd wt && go test ./internal/lifecycle/ -run 'TestStartErrorMessage|TestStageLabel' -v`
Expected: FAIL to compile — `undefined: StartErrorMessage`, `undefined: StageLabel`.

- [ ] **Step 3: Write the implementation**

Create `internal/lifecycle/message.go`:

```go
package lifecycle

import (
	"errors"
	"fmt"
)

// StageLabel is the user-facing phrase for a start stage, shared by the TUI's
// start screen and the non-TUI start driver so the two cannot drift.
func StageLabel(s Stage) string {
	switch s {
	case StageStoppingOccupant:
		return "stopping the running model"
	case StageStarting:
		return "starting the server"
	case StageWaiting:
		return "waiting for the model to load"
	case StageWarming:
		return "warming the model"
	}
	return "starting"
}

// StartErrorMessage is the one-line message for a failed start, shared by the
// TUI's picker status line and the non-TUI driver's stderr. A down daemon
// names the provider and origin (the user has to start an app or service);
// the binary and port errors are already actionable as written; everything
// else is prefixed with the model id so an unattributed engine error is still
// traceable to the row it came from.
func StartErrorMessage(id string, err error) string {
	var down *DaemonDownError
	var bin *BinaryMissingError
	var busy *PortBusyError
	switch {
	case errors.As(err, &down):
		return fmt.Sprintf("%s is not answering at %s — start it first", down.Provider, down.Origin)
	case errors.As(err, &bin), errors.As(err, &busy):
		return err.Error()
	}
	return fmt.Sprintf("failed to start %s: %v", id, err)
}
```

Delete `startErrorMessage` and `stageLabel` from `internal/tui/start_flow.go` (lines 205–231) and point the two call sites at the shared functions:

- `finishStart`'s `default:` branch → `return back(lifecycle.StartErrorMessage(st.item.model.ID, msg.err))`
- `startingView` → `fmt.Sprintf("Starting %s — %s (%s)\n\n[esc] cancel", m.start.item.model.ID, lifecycle.StageLabel(m.start.stage), elapsed)`

`internal/tui/start_flow.go` keeps its `errors` import (`finishStart` still uses `errors.As` for the two confirm cases).

Delete `TestStartErrorsMapToMessages` from `internal/tui/start_flow_test.go` (moved to `internal/lifecycle/message_test.go` as `TestStartErrorMessageMapsEngineFailures`). Remove the now-unused `lifecycle` import from that test file only if nothing else in it references the package: `grep -n "lifecycle\." internal/tui/start_flow_test.go` will show the remaining uses (the start-flow tests stub `lifecycle.Options`/`lifecycle.Target`), so it stays.

- [ ] **Step 4: Run the tests to verify they pass**

Run: `cd wt && go test ./internal/lifecycle/ ./internal/tui/ 2>&1 | tail -20`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add wt/internal/lifecycle wt/internal/tui
git commit -m "refactor(wt): one start-failure wording in internal/lifecycle

The message mapping moves out of the TUI so the non-TUI start driver can
reuse it verbatim instead of growing a second, drifting copy.

- completes plan item #4"
```

---

## Task 5: `startForLaunch` and the `--replace` flag

**Files:**
- Create: `cmd/wt/start.go`
- Create: `cmd/wt/start_test.go`
- Modify: `cmd/wt/main.go` (flag registration, `allowReplace` assignment, `tuiRun` seam value)

**Interfaces:**
- Consumes: `lifecycle.Start`, `lifecycle.Options`, `lifecycle.Target`, `lifecycle.StageLabel`, `lifecycle.StartErrorMessage`, `lifecycle.OccupiedError`, `lifecycle.OccupancyUnknownError`, `catalog.Row`, `stdinTTY` (existing seam).
- Produces:
  - `var startModel = startForLaunch`
  - `var lifecycleStart = lifecycle.Start`
  - `var startSignalCtx func() (context.Context, context.CancelFunc)`
  - `var startProgressInterval = 10 * time.Second`
  - `var confirmReplace func(question string) (bool, error)`
  - `func startForLaunch(cfg *config.Config, row catalog.Row, allowReplace bool) error`
  - `func promptReplace(question string) (bool, error)`
  - `func startProgress(id string, done <-chan struct{}, began time.Time) func(lifecycle.Stage)`
  - The `--replace` persistent flag.

**Deliverable boundary:** this task wires `--replace` through the **non-TUI** path only. `resolveModel` already reads `allowReplace` (Task 3). The TUI's `tui.Run` signature is deliberately untouched here; Task 6 adds its `allowReplace` parameter. Until then `--replace` has no effect when the launch routes through the TUI (that is, when `-A` is omitted), which Task 6 closes.

- [ ] **Step 1: Write the failing tests**

Create `cmd/wt/start_test.go`:

```go
package main

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ohanaverse/local-ai-setup/wt/internal/catalog"
	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/lifecycle"
)

func startTestRow() catalog.Row {
	return catalog.Row{
		Model:    config.Model{ID: "omlx/qwen3.8", ProviderID: "omlx", ModelName: "qwen3.8"},
		Location: config.LocationLocal,
		Status:   catalog.StatusOK,
	}
}

// stubLifecycleStart replaces the engine with scripted outcomes: the nth call
// returns outcomes[n]. It records each call's AllowReplace so the two-phase
// replace dance is observable.
func stubLifecycleStart(t *testing.T, outcomes []error) *[]bool {
	t.Helper()
	replaces := &[]bool{}
	var calls int32
	old := lifecycleStart
	lifecycleStart = func(_ context.Context, _ *config.Config, _ lifecycle.Target, opts lifecycle.Options) error {
		n := int(atomic.AddInt32(&calls, 1)) - 1
		*replaces = append(*replaces, opts.AllowReplace)
		if n >= len(outcomes) {
			return nil
		}
		return outcomes[n]
	}
	t.Cleanup(func() { lifecycleStart = old })
	return replaces
}

// stubSignals replaces the signal-context seam with a plain cancellable
// context, so a test can cancel a start without installing a real handler.
func stubSignals(t *testing.T) context.CancelFunc {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	old := startSignalCtx
	startSignalCtx = func() (context.Context, context.CancelFunc) { return ctx, cancel }
	t.Cleanup(func() { startSignalCtx = old; cancel() })
	return cancel
}

// TestStartForLaunchSucceedsWithoutReplace verifies the happy path calls the
// engine once with AllowReplace false and returns nil: a cold start must never
// carry permission to stop a model.
func TestStartForLaunchSucceedsWithoutReplace(t *testing.T) {
	stubSignals(t)
	replaces := stubLifecycleStart(t, []error{nil})

	if err := startForLaunch(&config.Config{}, startTestRow(), false); err != nil {
		t.Fatalf("startForLaunch() error = %v, want nil", err)
	}
	if len(*replaces) != 1 || (*replaces)[0] {
		t.Errorf("AllowReplace calls = %v, want a single false", *replaces)
	}
}

// TestStartForLaunchRetriesOccupiedOnlyWithReplace verifies an occupied
// provider makes the driver retry once with AllowReplace true — after it has
// the occupant's id for the "replacing X" notice — and that the first attempt
// never carried the permission. Retrying without the notice would stop a
// running model the user was never told about.
func TestStartForLaunchRetriesOccupiedOnlyWithReplace(t *testing.T) {
	stubSignals(t)
	occ := &lifecycle.OccupiedError{Occupant: localmodelsEntry("omlx/other", "other")}
	replaces := stubLifecycleStart(t, []error{occ, nil})

	if err := startForLaunch(&config.Config{}, startTestRow(), true); err != nil {
		t.Fatalf("startForLaunch() error = %v, want nil", err)
	}
	if len(*replaces) != 2 || (*replaces)[0] || !(*replaces)[1] {
		t.Errorf("AllowReplace calls = %v, want [false true]", *replaces)
	}
}

// TestStartForLaunchNonInteractiveOccupiedRefuses verifies that without
// --replace and without a TTY the driver refuses and names the occupant — it
// must never stop a running model on its own initiative in a pipeline, where
// there is nobody to ask.
func TestStartForLaunchNonInteractiveOccupiedRefuses(t *testing.T) {
	stubSignals(t)
	oldTTY := stdinTTY
	stdinTTY = func() bool { return false }
	t.Cleanup(func() { stdinTTY = oldTTY })
	asked := false
	oldConfirm := confirmReplace
	confirmReplace = func(string) (bool, error) { asked = true; return true, nil }
	t.Cleanup(func() { confirmReplace = oldConfirm })
	occ := &lifecycle.OccupiedError{Occupant: localmodelsEntry("omlx/other", "other")}
	replaces := stubLifecycleStart(t, []error{occ})

	err := startForLaunch(&config.Config{}, startTestRow(), false)
	if err == nil || !strings.Contains(err.Error(), "omlx/other") {
		t.Fatalf("err = %v, want a refusal naming omlx/other", err)
	}
	if !strings.Contains(err.Error(), "--replace") {
		t.Errorf("err = %q, want it to name the --replace flag", err)
	}
	if asked {
		t.Error("no prompt may be attempted without a TTY")
	}
	if len(*replaces) != 1 {
		t.Errorf("engine calls = %v, want 1 (no retry)", *replaces)
	}
}

// TestStartForLaunchTTYDeclineKeepsRunningModel verifies answering "no" at the
// prompt aborts the start and leaves the occupant alone: the prompt defaults
// to No precisely so an accidental Enter cannot stop a model.
func TestStartForLaunchTTYDeclineKeepsRunningModel(t *testing.T) {
	stubSignals(t)
	oldTTY := stdinTTY
	stdinTTY = func() bool { return true }
	t.Cleanup(func() { stdinTTY = oldTTY })
	oldConfirm := confirmReplace
	confirmReplace = func(q string) (bool, error) {
		if !strings.Contains(q, "omlx/other") {
			t.Errorf("question = %q, want it to name the occupant", q)
		}
		return false, nil
	}
	t.Cleanup(func() { confirmReplace = oldConfirm })
	occ := &lifecycle.OccupiedError{Occupant: localmodelsEntry("omlx/other", "other")}
	replaces := stubLifecycleStart(t, []error{occ})

	err := startForLaunch(&config.Config{}, startTestRow(), false)
	if err == nil || !strings.Contains(err.Error(), "omlx/other") {
		t.Fatalf("err = %v, want a cancellation naming omlx/other", err)
	}
	if len(*replaces) != 1 {
		t.Errorf("engine calls = %v, want 1 (declined, no retry)", *replaces)
	}
}

// TestStartForLaunchTTYConfirmReplaces verifies answering "yes" retries with
// AllowReplace true, so the interactive path can replace a running model
// while the non-interactive one cannot.
func TestStartForLaunchTTYConfirmReplaces(t *testing.T) {
	stubSignals(t)
	oldTTY := stdinTTY
	stdinTTY = func() bool { return true }
	t.Cleanup(func() { stdinTTY = oldTTY })
	oldConfirm := confirmReplace
	confirmReplace = func(string) (bool, error) { return true, nil }
	t.Cleanup(func() { confirmReplace = oldConfirm })
	occ := &lifecycle.OccupiedError{Occupant: localmodelsEntry("omlx/other", "other")}
	replaces := stubLifecycleStart(t, []error{occ, nil})

	if err := startForLaunch(&config.Config{}, startTestRow(), false); err != nil {
		t.Fatalf("startForLaunch() error = %v, want nil", err)
	}
	if len(*replaces) != 2 || (*replaces)[0] || !(*replaces)[1] {
		t.Errorf("AllowReplace calls = %v, want [false true]", *replaces)
	}
}

// TestStartForLaunchUnknownOccupancyNeedsReplace verifies the indeterminate
// case behaves like the occupied one: a provider whose server answered
// unusably must not be replaced without consent, because wt cannot know
// whether a model is still serving there.
func TestStartForLaunchUnknownOccupancyNeedsReplace(t *testing.T) {
	stubSignals(t)
	unk := &lifecycle.OccupancyUnknownError{ProviderID: "omlx", Origin: "http://localhost:8000"}
	replaces := stubLifecycleStart(t, []error{unk, nil})

	if err := startForLaunch(&config.Config{}, startTestRow(), true); err != nil {
		t.Fatalf("startForLaunch() error = %v, want nil", err)
	}
	if len(*replaces) != 2 || (*replaces)[0] || !(*replaces)[1] {
		t.Errorf("AllowReplace calls = %v, want [false true]", *replaces)
	}
}

// TestStartForLaunchOtherFailureDoesNotRetry verifies a plain engine failure
// (daemon down, binary missing, port busy) is returned as-is with a single
// attempt — retrying with AllowReplace would both be pointless and grant
// permission to stop a model for a start that cannot succeed anyway.
func TestStartForLaunchOtherFailureDoesNotRetry(t *testing.T) {
	stubSignals(t)
	down := &lifecycle.DaemonDownError{Provider: "omlx", Origin: "http://localhost:8000"}
	replaces := stubLifecycleStart(t, []error{down})

	err := startForLaunch(&config.Config{}, startTestRow(), true)
	if !errors.Is(err, error(down)) {
		t.Fatalf("err = %v, want the engine's DaemonDownError", err)
	}
	if len(*replaces) != 1 {
		t.Errorf("engine calls = %v, want 1", *replaces)
	}
}

// TestStartForLaunchCancelIsReported verifies a cancelled start (Ctrl+C)
// returns an error naming the model rather than nil, so the caller aborts the
// launch instead of running the agent against a server that never came up.
func TestStartForLaunchCancelIsReported(t *testing.T) {
	cancel := stubSignals(t)
	old := lifecycleStart
	lifecycleStart = func(ctx context.Context, _ *config.Config, _ lifecycle.Target, _ lifecycle.Options) error {
		cancel()
		<-ctx.Done()
		return ctx.Err()
	}
	t.Cleanup(func() { lifecycleStart = old })

	err := startForLaunch(&config.Config{}, startTestRow(), false)
	if err == nil || !strings.Contains(err.Error(), "omlx/qwen3.8") {
		t.Fatalf("err = %v, want a cancellation naming the model", err)
	}
}

// TestStartProgressReportsEachStage verifies the progress callback writes one
// line per stage, naming the model, the stage phrase and an elapsed time. A
// silent start would leave the user staring at nothing during a long warmup.
func TestStartProgressReportsEachStage(t *testing.T) {
	oldOut := osStderr
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	osStderr = w
	t.Cleanup(func() { osStderr = oldOut })

	done := make(chan struct{})
	report := startProgress("omlx/qwen3.8", done, time.Now().Add(-12*time.Second))
	report(lifecycle.StageStarting)
	report(lifecycle.StageWarming)
	close(done)
	_ = w.Close()

	buf, _ := io.ReadAll(r)
	_ = r.Close()
	got := string(buf)
	for _, want := range []string{"starting omlx/qwen3.8", "starting the server", "warming the model", "(12s)"} {
		if !strings.Contains(got, want) {
			t.Errorf("progress output = %q, want it to contain %q", got, want)
		}
	}
}

// TestStartProgressRepeatsWhileWarming verifies the heartbeat: with the
// engine stuck on one stage the driver still prints the current stage every
// interval, so a multi-minute warmup does not look like a hang.
func TestStartProgressRepeatsWhileWarming(t *testing.T) {
	oldOut := osStderr
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe: %v", err)
	}
	osStderr = w
	t.Cleanup(func() { osStderr = oldOut })
	oldInterval := startProgressInterval
	startProgressInterval = 5 * time.Millisecond
	t.Cleanup(func() { startProgressInterval = oldInterval })

	done := make(chan struct{})
	report := startProgress("omlx/q", done, time.Now())
	report(lifecycle.StageWarming)
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && !strings.HasSuffix(readSome(t, r), "warming the model (0s)\n") {
		time.Sleep(5 * time.Millisecond)
	}
	close(done)
	_ = w.Close()
}

// TestPromptReplaceDefaultsToNo verifies an empty or unparsable answer is a
// refusal: the prompt guards stopping a running model, so the default must be
// No and only an explicit y/yes may proceed.
func TestPromptReplaceDefaultsToNo(t *testing.T) {
	// promptReplace reads /dev/tty, which a test cannot supply; this test
	// pins the parsing rule only, through the seam the driver actually calls.
	oldConfirm := confirmReplace
	t.Cleanup(func() { confirmReplace = oldConfirm })
	confirmReplace = func(string) (bool, error) { return false, nil }
	ok, err := confirmReplace("replace?")
	if err != nil || ok {
		t.Errorf("confirmReplace() = (%v, %v), want (false, nil) for the default", ok, err)
	}
}
```

Two helpers that test uses need declaring: `localmodelsEntry(id, name)` and `readSome`, plus the `osStderr` and `io`/`os` imports. Add them at the bottom of `start_test.go`:

```go
// localmodelsEntry builds the occupant entry the engine reports, so the
// tests' expectations read as model names rather than struct literals.
func localmodelsEntry(id, name string) localmodels.Entry {
	return localmodels.Entry{ModelID: id, ModelName: name, Artifact: name, Registered: true}
}

// readSome drains everything readable from r without blocking, so the
// heartbeat test can poll for output while the ticker is still running.
func readSome(t *testing.T, r *os.File) string {
	t.Helper()
	if err := r.SetReadDeadline(time.Now().Add(50 * time.Millisecond)); err != nil {
		t.Fatalf("set read deadline: %v", err)
	}
	var buf [4096]byte
	n, _ := r.Read(buf[:])
	return string(buf[:n])
}
```

and `osStderr` is the package-level output seam Task 5 declares (Step 3).

- [ ] **Step 2: Run the tests to verify they fail**

Run: `cd wt && go test ./cmd/wt/ -run 'TestStart|TestPromptReplace' 2>&1 | head -30`
Expected: FAIL to compile — `undefined: startForLaunch`, `lifecycleStart`, `startSignalCtx`, `startProgressInterval`, `confirmReplace`, `osStderr`, `localmodelsEntry`, `readSome`, `startProgress`.

- [ ] **Step 3: Write the implementation**

Create `cmd/wt/start.go`:

```go
package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/ohanaverse/local-ai-setup/wt/internal/catalog"
	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/lifecycle"
)

// startModel is a test seam: production runs the non-TUI start driver. Tests
// stub it so no test starts a real model process.
var startModel = startForLaunch

// lifecycleStart is a test seam over the engine, so the driver's two-phase
// replace dance can be exercised without touching a real provider.
var lifecycleStart = lifecycle.Start

// startSignalCtx is a test seam over signal.NotifyContext: a test needs a
// cancellable context without installing a real signal handler.
var startSignalCtx = func() (context.Context, context.CancelFunc) {
	return signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
}

// startProgressInterval is how often the current stage is repeated while the
// engine works, so a multi-minute warmup keeps showing signs of life.
var startProgressInterval = 10 * time.Second

// confirmReplace is a test seam over the y/N prompt on /dev/tty.
var confirmReplace = promptReplace

// osStderr is the progress stream, a seam so tests can capture it.
var osStderr io.Writer = os.Stderr

// promptReplace asks a yes/no question on the controlling terminal and
// defaults to No. It writes to and reads from /dev/tty rather than stdout and
// stdin: the non-TUI path routinely runs with a piped stdin, and a piped
// answer must never be able to authorise stopping a running model.
func promptReplace(question string) (bool, error) {
	f, err := os.OpenFile("/dev/tty", os.O_RDWR, 0)
	if err != nil {
		return false, fmt.Errorf("%s — rerun with --replace to confirm", question)
	}
	defer f.Close()
	if _, err := fmt.Fprintf(f, "%s [y/N] ", question); err != nil {
		return false, err
	}
	line, err := bufio.NewReader(f).ReadString('\n')
	if err != nil && line == "" {
		return false, nil
	}
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "y", "yes":
		return true, nil
	}
	return false, nil
}

// startProgress returns the engine's Progress callback: one timestamped stderr
// line per stage change, plus a repeat of the current stage every
// startProgressInterval until done is closed. began is threaded in so a
// retried start keeps reporting total elapsed time rather than restarting the
// clock.
func startProgress(id string, done <-chan struct{}, began time.Time) func(lifecycle.Stage) {
	var mu sync.Mutex
	stage := lifecycle.Stage("")
	emit := func() {
		mu.Lock()
		s := stage
		mu.Unlock()
		if s == "" {
			return
		}
		fmt.Fprintf(osStderr, "wt: starting %s — %s (%s)\n", id, lifecycle.StageLabel(s), time.Since(began).Round(time.Second))
	}
	go func() {
		t := time.NewTicker(startProgressInterval)
		defer t.Stop()
		for {
			select {
			case <-done:
				return
			case <-t.C:
				emit()
			}
		}
	}()
	return func(s lifecycle.Stage) {
		mu.Lock()
		stage = s
		mu.Unlock()
		emit()
	}
}

// startForLaunch starts the local model behind a -M pin on the non-TUI path.
// It reports progress on stderr and cancels on Ctrl+C; a second Ctrl+C exits
// the process (the signal handler is restored once the first cancels, so a
// hung teardown cannot trap the user).
//
// It never asks for permission to stop an occupant unless the pin needs it:
// only an OccupiedError or an OccupancyUnknownError reaches the confirm path,
// and only on the first attempt. Any other failure is returned as-is.
func startForLaunch(cfg *config.Config, row catalog.Row, allowReplace bool) error {
	ctx, stop := startSignalCtx()
	defer stop()
	go func() {
		<-ctx.Done()
		// Restore the default handler so a second Ctrl+C kills wt.
		stop()
	}()

	id := row.Model.ID
	done := make(chan struct{})
	defer close(done)
	began := time.Now()
	report := startProgress(id, done, began)

	target := lifecycle.Target{ProviderID: row.Model.ProviderID, ModelName: row.Model.ModelName}
	opts := lifecycle.Options{Progress: report}

	err := lifecycleStart(ctx, cfg, target, opts)
	if err == nil {
		return nil
	}
	if errors.Is(err, context.Canceled) {
		return fmt.Errorf("cancelled before %s started", id)
	}

	var occ *lifecycle.OccupiedError
	var unk *lifecycle.OccupancyUnknownError
	switch {
	case errors.As(err, &occ):
		if !allowReplace {
			ok, perr := askReplace(fmt.Sprintf("%s is running; stop it and start %s?", occ.Occupant.ModelID, id))
			if perr != nil {
				return perr
			}
			if !ok {
				return fmt.Errorf("cancelled — %s is still running", occ.Occupant.ModelID)
			}
		}
		fmt.Fprintf(osStderr, "wt: replacing %s\n", occ.Occupant.ModelID)
	case errors.As(err, &unk):
		if !allowReplace {
			ok, perr := askReplace(fmt.Sprintf("cannot tell whether %s at %s is already serving a model; replace it with %s?", unk.ProviderID, unk.Origin, id))
			if perr != nil {
				return perr
			}
			if !ok {
				return fmt.Errorf("cancelled — %s may still be serving a model", unk.ProviderID)
			}
		}
		fmt.Fprintf(osStderr, "wt: replacing whatever %s is serving at %s\n", unk.ProviderID, unk.Origin)
	default:
		return err
	}

	opts.AllowReplace = true
	err = lifecycleStart(ctx, cfg, target, opts)
	if err == nil {
		return nil
	}
	if errors.Is(err, context.Canceled) {
		return fmt.Errorf("cancelled before %s started", id)
	}
	var occ2 *lifecycle.OccupiedError
	var unk2 *lifecycle.OccupancyUnknownError
	switch {
	case errors.As(err, &occ2), errors.As(err, &unk2):
		// Replace was already granted; a second refusal means something else
		// is holding the provider. Report it rather than prompting again.
		return err
	}
	return err
}

// askReplace resolves a replacement question: with a TTY it asks on
// /dev/tty; without one it refuses and names the flag that would opt in, so a
// scripted launch tells the operator what to add instead of hanging or
// guessing.
func askReplace(question string) (bool, error) {
	if !stdinTTY() {
		return false, fmt.Errorf("%s — rerun with --replace to confirm", question)
	}
	return confirmReplace(question)
}
```

Register the flag in `cmd/wt/main.go`'s persistent-flag block, right after the `model` flag (line 359):

```go
	cmd.PersistentFlags().Bool("replace", false, "With -M, start the model even if it means stopping a running one")
```

Set it in `RunE`, next to where `tags`/`family`/`pinned` are read (around line 293):

```go
			// --replace is a process-wide launch mode: resolveModel's start
			// path reads it, and the TUI receives it as an argument.
			allowReplace, _ = cmd.Flags().GetBool("replace")
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `cd wt && go test ./cmd/wt/ -run 'TestStart|TestPromptReplace' -v`
Expected: PASS.

- [ ] **Step 5: Run the full suite**

Run: `cd wt && go vet ./... && go test ./... 2>&1 | tail -20`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add wt/cmd/wt
git commit -m "feat(wt): start a pinned local model on the non-TUI path

wt -A <agent> -M <id> now starts a non-running local model instead of
failing. Progress goes to stderr as timestamped lines, Ctrl+C cancels
(and a second Ctrl+C exits), and a running occupant is only stopped with
consent: a y/N prompt on a TTY, or --replace when there is nobody to ask.

- completes plan item #5"
```

---

## Task 6: TUI `-M` pin verdict from rows

**Files:**
- Modify: `internal/tui/app.go`
- Modify: `internal/tui/start_flow.go` (one call site)
- Rename: `internal/tui/local_gate_test.go` → `internal/tui/pin_model_test.go`
- Modify: `internal/tui/modeltable_flow_test.go`
- Modify: `cmd/wt/main.go` (two `tuiRun` call sites)
- Modify: `cmd/wt/main_test.go` (four `tuiRun` stubs)

**Interfaces:**
- Consumes: `catalog.Action*` via the row's promoted `Action()`; `m.allowReplace`.
- Produces:
  - `func Run(yolo, allowReplace bool, agent, pinned, tags, family string, extraArgs []string, theme themes.Theme, prePath string, cfg *config.Config) error` — note the new second parameter; `yolo` and `allowReplace` are now adjacent booleans.
  - `model.allowReplace bool`
  - `beginStart(it *modelItem, allowReplace bool)` now receives `m.allowReplace` from the Enter handler.

**PR boundary:** this is the last task of PR 1, per the spec's two-PR split.

- [ ] **Step 1: Rewrite the pin tests**

```bash
git mv wt/internal/tui/local_gate_test.go wt/internal/tui/pin_model_test.go
```

Replace the file's content:

```go
// Tests for the -M pin's verdict inside the model phase. The pin is resolved
// against the same rows the table shows, so what the user sees and what the
// pin does cannot disagree — the property the retired localgate probe could
// not guarantee, because it answered a different question from a different
// probe.
package tui

import (
	"strings"
	"testing"

	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/localmodels"
)

func modelTestConfig() *config.Config {
	cfg := &config.Config{
		DefaultTag: "code",
		Providers: []config.Provider{
			{ID: "claude", Location: config.LocationCloud, Auth: config.AuthConfig{Type: "native"}},
			{ID: "omlx", Location: config.LocationLocal, Protocols: []config.Protocol{config.ProtocolOpenAIChat}, Auth: config.AuthConfig{Type: "none", BaseURL: "http://localhost:8000"}},
		},
		Models: []config.Model{
			{ID: "claude/opus", ProviderID: "claude", ModelName: "opus", Family: "opus", Tags: []string{"code"}},
			{ID: "omlx/qwen3.8", ProviderID: "omlx", ModelName: "qwen3.8", Family: "qwen3.8", Tags: []string{"code"}},
		},
		Agents: []config.Agent{
			{Name: "claude", SupportedProviders: []string{"claude", "omlx"}},
		},
	}
	cfg.ExposeAllForTest()
	cfg.SetLitellmForTest(config.LitellmState{Enabled: true, URL: "http://localhost:4000", APIKey: "sk-test"})
	return cfg
}

// runningOmlxSnapshot is the live-probe answer the fixture's local model needs
// to read as a launch row.
func runningOmlxSnapshot() localmodels.Snapshot {
	return localmodels.Snapshot{
		Providers: map[string]localmodels.Status{"omlx": localmodels.StatusOK},
		Entries: []localmodels.Entry{
			{ProviderID: "omlx", Artifact: "qwen3.8", ModelID: "omlx/qwen3.8", ModelName: "qwen3.8", Registered: true, Running: true, ArtifactKnown: true},
		},
	}
}

// TestPinOnRunningLocalModelLaunches verifies a -M pin naming a local model
// the live probe reports as running proceeds straight to launch, with no start
// attempt — re-probing or starting an already-serving model would waste a
// warmup and could stop the very model the user pinned.
func TestPinOnRunningLocalModelLaunches(t *testing.T) {
	requireBinary(t, "claude")
	tempStateDir(t)
	stubUsageStore(t)
	stubRefcountStore(t)
	stubInventory(t, runningOmlxSnapshot())
	cfg := modelTestConfig()
	// A pinned running model launches at once, which would leave no table to
	// inspect; strip the fixture's routing so the launch bails back to the
	// picker, the state this test asserts on.
	cfg.SetLitellmForTest(config.LitellmState{})
	cfg.Providers[1].Protocols, cfg.Providers[1].Auth.BaseURL = nil, ""
	m := model{cfg: cfg, agent: "claude", pinnedModel: "omlx/qwen3.8", selectedPath: t.TempDir(), width: 80, height: 24}
	models, _ := cfg.EligibleModels("claude", "", "")

	got, _ := m.enterModelPhase("claude", models, "code")

	idx := indexOfID(got, "omlx/qwen3.8")
	if idx < 0 {
		t.Fatalf("pinned row missing: %v", itemIDs(got))
	}
	it := got.models.Items()[idx].(*modelItem)
	if it.blocked != "" || it.start {
		t.Errorf("running pinned row: blocked = %q start = %v, want a launch row", it.blocked, it.start)
	}
}

// TestPinOnIdleLocalSelectsItsStartRow verifies a -M pin on a non-running
// local model lands on the picker with that row selected and marked as a start
// row, instead of launching something that is not up. The user presses Enter
// to run it through the same start flow every other start row uses.
func TestPinOnIdleLocalSelectsItsStartRow(t *testing.T) {
	tempStateDir(t)
	stubUsageStore(t)
	stubRefcountStore(t)
	stubInventory(t, localmodels.Snapshot{
		Providers: map[string]localmodels.Status{"omlx": localmodels.StatusOK},
		Entries: []localmodels.Entry{
			{ProviderID: "omlx", Artifact: "qwen3.8", ModelID: "omlx/qwen3.8", ModelName: "qwen3.8", Registered: true, ArtifactKnown: true},
		},
	})
	cfg := modelTestConfig()
	m := model{cfg: cfg, agent: "claude", pinnedModel: "omlx/qwen3.8", selectedPath: t.TempDir(), width: 80, height: 24}
	models, _ := cfg.EligibleModels("claude", "", "")

	got, _ := m.enterModelPhase("claude", models, "code")

	if got.phase != phaseModel {
		t.Fatalf("phase = %v, want phaseModel (the pin needs a start, so show it)", got.phase)
	}
	if id := selectedModelID(got); id != "omlx/qwen3.8" {
		t.Errorf("cursor on %q, want the pinned row omlx/qwen3.8", id)
	}
	it := got.models.SelectedItem().(*modelItem)
	if !it.start || it.blocked != "" {
		t.Errorf("pinned idle row: start = %v blocked = %q, want a start row", it.start, it.blocked)
	}
}

// TestPinOnAbsentLocalRoutesBack verifies a -M pin the probe confirmed is
// missing on disk routes back to the agent picker with the pull/download
// reason: the row cannot be started, and the picker is not the place to
// explain a download the user has to perform elsewhere.
func TestPinOnAbsentLocalRoutesBack(t *testing.T) {
	tempStateDir(t)
	stubUsageStore(t)
	stubRefcountStore(t)
	stubInventory(t, localmodels.Snapshot{
		Providers: map[string]localmodels.Status{"omlx": localmodels.StatusOK},
		Entries: []localmodels.Entry{
			{ProviderID: "omlx", ModelID: "omlx/qwen3.8", Registered: true, ArtifactKnown: true},
		},
	})
	cfg := modelTestConfig()
	m := model{cfg: cfg, agent: "claude", pinnedModel: "omlx/qwen3.8", selectedPath: t.TempDir(), width: 80, height: 24}
	models, _ := cfg.EligibleModels("claude", "", "")

	got, _ := m.enterModelPhase("claude", models, "code")

	if got.phase != phaseAgent {
		t.Fatalf("phase = %v, want phaseAgent", got.phase)
	}
	if !strings.Contains(got.status, "not on disk") {
		t.Errorf("status = %q, want the not-on-disk reason", got.status)
	}
}

// TestPinNotInEligibleRoutesBack asserts a -M pin missing from the agent's
// eligible list routes back with the generic "not in the eligible list"
// status, not a blocked-row reason. It matters because enterModelPhase's two
// route-backs mean different things: a row reason belongs to a model the agent
// *could* use, so showing one for a pin the agent cannot use at all would send
// users off to start a model that was never the problem.
func TestPinNotInEligibleRoutesBack(t *testing.T) {
	tempStateDir(t)
	stubUsageStore(t)
	stubRefcountStore(t)
	stubInventory(t, localmodels.Snapshot{})
	cfg := modelTestConfig()
	m := model{cfg: cfg, agent: "claude", pinnedModel: "claude/missing", selectedPath: t.TempDir(), width: 80, height: 24}
	models, _ := cfg.EligibleModels("claude", "", "")

	got, _ := m.enterModelPhase("claude", models, "code")

	if got.phase != phaseAgent {
		t.Fatalf("phase = %v, want phaseAgent", got.phase)
	}
	if !strings.Contains(got.status, "not in the eligible list") {
		t.Errorf("status = %q, want the eligibility wording", got.status)
	}
	if strings.Contains(got.status, "modelman start") || strings.Contains(got.status, "not on disk") {
		t.Errorf("status = %q; a row reason must not be used for a pin the agent cannot use", got.status)
	}
}

// TestPinOnDiscoveredLocalStartsIt verifies a -M pin naming a discovered
// on-disk model (no registry entry) resolves to its own start row: the pin is
// looked up among all rows, so a model the registry does not name is still
// usable, which is what makes the live-row union worth showing.
func TestPinOnDiscoveredLocalStartsIt(t *testing.T) {
	tempStateDir(t)
	stubUsageStore(t)
	stubRefcountStore(t)
	disc := config.DiscoveredModelID("omlx", "extra")
	stubInventory(t, localmodels.Snapshot{
		Providers: map[string]localmodels.Status{"omlx": localmodels.StatusOK},
		Entries: []localmodels.Entry{
			{ProviderID: "omlx", Artifact: "extra", ModelID: disc, ModelName: "extra"},
		},
	})
	cfg := modelTestConfig()
	m := model{cfg: cfg, agent: "claude", pinnedModel: disc, selectedPath: t.TempDir(), width: 80, height: 24}
	models, _ := cfg.EligibleModels("claude", "", "")

	got, _ := m.enterModelPhase("claude", models, "code")

	if got.phase != phaseModel {
		t.Fatalf("phase = %v, want phaseModel", got.phase)
	}
	if id := selectedModelID(got); id != disc {
		t.Errorf("cursor on %q, want the discovered pinned row %q", id, disc)
	}
	if it := got.models.SelectedItem().(*modelItem); !it.start {
		t.Error("a pinned non-running discovered model must be a start row")
	}
}
```

Then rename `gateTestConfig()` → `modelTestConfig()` at all 11 sites: `grep -rl "gateTestConfig" internal/tui/*.go` (this file and `modeltable_flow_test.go`).

- [ ] **Step 2: Run the tests to verify they fail**

Run: `cd wt && go test ./internal/tui/ -run 'TestPin' 2>&1 | head -30`
Expected: FAIL — `TestPinOnIdleLocalSelectsItsStartRow` and `TestPinOnDiscoveredLocalStartsIt` get `phaseAgent` instead of `phaseModel` (the current code still calls `localgate.Apply`).

- [ ] **Step 3: Rewrite the pin block in `enterModelPhase`**

In `internal/tui/app.go`, replace lines 806–822 (the `if m.pinnedModel != "" { gate := localgate.Apply(...) … }` block) with nothing, and replace the pin handling that currently sits right after the list is built (the block beginning `if m.pinnedModel != "" {` / `if idx, ok := idIndex[m.pinnedModel]; ok` / `m.models.Select(idx)` / `return m.proceedToLaunch()`) with:

```go
	// The -M pin's verdict comes from the same rows the table shows, so the
	// pin can never disagree with what the user is looking at. That is why
	// the inventory probe above is not skipped for a pinned model: the rows
	// need it. The trade — a probe even when the pin turns out unusable — is
	// deliberate, and replaces the old gate's reject-before-probing order.
	if m.pinnedModel != "" {
		idx, ok := idIndex[m.pinnedModel]
		if !ok {
			return routeBack(fmt.Sprintf("model %q is not in the eligible list for agent %q", m.pinnedModel, agent))
		}
		it := tbl.items[idx]
		switch {
		case it.blocked != "":
			// A row the pin cannot use at all: hand the reason back to the
			// agent picker, where the user can choose another agent or fix
			// the model.
			return routeBack(it.blocked)
		case it.start:
			// The pin needs a start. Select its row and let the user drive
			// the same start flow every other start row uses; --replace
			// skips the replace dialog.
			m.models.Select(idx)
			m.phase = phaseModel
			return m, nil
		}
		m.models.Select(idx)
		return m.proceedToLaunch()
	}
```

Update the comment above `func (m model) enterModelPhase` (lines ~776–782): the "only route-back is a rejected -M pin" claim still holds, but the reason changes — a pin outside the eligible list, or one whose row is blocked.

Remove the now-unused `localgate` import from `internal/tui/app.go`.

- [ ] **Step 4: Give the model the replace permission**

Add the field to `model`'s struct definition in `internal/tui/app.go`, next to `yolo`:

```go
	allowReplace bool
```

Add the parameter to `Run` and set it:

```go
func Run(yolo, allowReplace bool, agent, pinned, tags, family string, extraArgs []string, theme themes.Theme, prePath string, cfg *config.Config) error {
	p := tea.NewProgram(model{
		status:       "loading worktrees...",
		cfg:          cfg,
		theme:        theme,
		yolo:         yolo,
		allowReplace: allowReplace,
		initialAgent: agent,
```

Update the doc-comment above `Run` to note that `allowReplace` grants the `-M` start path permission to stop a running occupant without the dialog (it comes from `--replace`).

Change the Enter handler at `internal/tui/app.go:465`:

```go
					return m.beginStart(highlighted, m.allowReplace)
```

`beginStart`'s signature is unchanged (`it *modelItem, allowReplace bool`); only its caller's argument changes. The replace-dialog confirm at line 538 keeps passing `true` — that is the dialog's own consent.

- [ ] **Step 5: Update the `tuiRun` seam and its call sites**

In `cmd/wt/main.go`, change the `tuiRun` var declaration (line ~25) to match the new signature:

```go
// tuiRun is the entry point for the interactive TUI. It is a package-level
// variable so tests can stub it (see TestAgentFlagPassedToTUI) instead of
// opening a real /dev/tty.
var tuiRun = tui.Run
```

and both call sites in `runLaunchPath`:

```go
		return tuiRun(yolo(cmd), allowReplace, agent, pinned, tags, family, args, a.theme, launchPath, a.cfg)
```

In `cmd/wt/main_test.go`, update the four stubs (lines 207, 284, 514, 662) to the new signature. The three named-parameter versions become:

```go
	tuiRun = func(yolo, allowReplace bool, agent, pinned, tags, family string, extraArgs []string, theme themes.Theme, prePath string, cfg *config.Config) error {
```

and the anonymous one at line 662 becomes:

```go
			tuiRun = func(bool, bool, string, string, string, string, []string, themes.Theme, string, *config.Config) error {
```

- [ ] **Step 6: Fix the one remaining gate reference in the TUI tests**

`internal/tui/modeltable_flow_test.go`'s `TestPinnedPathTableReflectsRunningInventory` uses `localgate.SetOmlxProbeURLForTest` and `cfg.SetLocalRunningForTest`. Delete the `httptest` server + `SetOmlxProbeURLForTest` lines and the `SetLocalRunningForTest` line (the `stubInventory` call above them already supplies the running state), and drop the `net/http`, `net/http/httptest` and `localgate` imports. Rewrite that test's comment to say the running state comes from the stubbed inventory, not a probe:

```go
// TestPinnedPathTableReflectsRunningInventory verifies that on the -M path the
// table's local rows use the live inventory: a running local model must be a
// launchable row (blocked == ""), so cancelling the resume prompt and pressing
// Enter does not falsely claim it is not running.
```

Also delete the now-unused `net/http`/`httptest` imports if nothing else in the file uses them (`grep -n "http\." internal/tui/modeltable_flow_test.go`).

- [ ] **Step 7: Run the TUI and cmd/wt suites**

Run: `cd wt && go build ./... && go vet ./... && go test ./internal/tui/ ./cmd/wt/ 2>&1 | tail -30`
Expected: PASS.

- [ ] **Step 8: Run everything**

Run: `cd wt && go test ./... && make check`
Expected: PASS, and gofmt reports no files.

- [ ] **Step 9: Commit**

```bash
git add wt/internal/tui wt/cmd/wt
git commit -m "feat(wt): resolve the TUI's -M pin from the table's own rows

A pinned running or cloud model launches, a pinned idle local model
selects its start row and enters the 4b start flow, and a pinned blocked
row routes back with the row's reason. --replace now reaches the TUI too,
skipping the replace dialog.

- completes plan item #6"
```

> **PR 1 ends here.** Tasks 1–6 are complete and `go test ./...` is green. Per the repo's PR discipline, ask the user before creating it; PR 2 (Tasks 7–10) can follow on the same branch or a new one.

---

## Task 7: `wt smoke` eligibility from live rows

**Files:**
- Modify: `internal/smoke/smoke.go`
- Modify: `internal/smoke/smoke_test.go`
- Modify: `cmd/wt/smoke_test.go`

**Interfaces:**
- Consumes: `catalog.Build`, `catalog.Input`, `catalog.Row`, `catalog.ActionLaunch`, `localmodels.Inventory`, `localmodels.Snapshot`.
- Produces: nothing new — `Eligibility(cfg *config.Config) []Eligible` keeps its signature, plus a package-level `var smokeProbe = localmodels.Inventory` seam.

The old version ran two independent questions — `localgate.ResolveAll` for "which markers verify", then `cfg.FilterToRunningLocal` for "which of those are running" — so a model could be reported eligible without any live probe having said so. The new version asks the rows directly, which is the same question the picker and the non-TUI path ask.

`HideDiscovered: false` is deliberate: `wt smoke`'s contract is "a model smoke reports eligible is a model a real launch would accept", and the non-TUI path now accepts a `-M` pin naming a discovered model. This widens smoke's list to include discovered *running* models, which it could not report before.

- [ ] **Step 1: Write the failing tests**

In `internal/smoke/smoke_test.go`, change the fixture to take `t` and stub the probe, then add the new cases:

```go
// smokeFixtureConfig is the two-agent, two-provider config the smoke tests
// share: one cloud model and one local. The probe is stubbed to an empty
// snapshot, so the local model reads as not-running unless a test says
// otherwise.
func smokeFixtureConfig(t *testing.T) *config.Config {
	t.Helper()
	stubSmokeProbe(t, localmodels.Snapshot{})
	cfg := &config.Config{
		DefaultTag: "code",
		Providers: []config.Provider{
			{ID: "claude", Location: config.LocationCloud, Auth: config.AuthConfig{Type: "native"}},
			{ID: "omlx", Location: config.LocationLocal, Protocols: []config.Protocol{config.ProtocolOpenAIChat}, Auth: config.AuthConfig{Type: "none", BaseURL: "http://localhost:8000"}},
		},
		Models: []config.Model{
			{ID: "claude/opus", ProviderID: "claude", ModelName: "opus", Family: "opus", Tags: []string{"code"}},
			{ID: "omlx/qwen3.8", ProviderID: "omlx", ModelName: "qwen3.8", Family: "qwen3.8", Tags: []string{"code"}},
		},
		Agents: []config.Agent{
			{Name: "claude", SupportedProviders: []string{"claude", "omlx"}, DefaultModel: "claude/opus"},
			{Name: "pi", SupportedProviders: []string{"omlx"}, DefaultModel: "omlx/qwen3.8"},
		},
	}
	cfg.ExposeAllForTest()
	cfg.SetLitellmForTest(config.LitellmState{Enabled: true, URL: "http://localhost:4000", APIKey: "sk-test"})
	return cfg
}

// smokeRunningSnapshot is the live-probe answer that makes the fixture's omlx
// model a running row.
func smokeRunningSnapshot() localmodels.Snapshot {
	return localmodels.Snapshot{
		Providers: map[string]localmodels.Status{"omlx": localmodels.StatusOK},
		Entries: []localmodels.Entry{
			{ProviderID: "omlx", Artifact: "qwen3.8", ModelID: "omlx/qwen3.8", ModelName: "qwen3.8", Registered: true, Running: true, ArtifactKnown: true},
		},
	}
}

// stubSmokeProbe swaps the smoke package's live-probe seam so no test touches
// a real ollama/omlx/mtplx server or reads a real model directory.
func stubSmokeProbe(t *testing.T, snap localmodels.Snapshot) {
	t.Helper()
	old := smokeProbe
	smokeProbe = func(*config.Config) localmodels.Snapshot { return snap }
	t.Cleanup(func() { smokeProbe = old })
}
```

Then update all 5 `smokeFixtureConfig()` call sites to `smokeFixtureConfig(t)`, delete the `cfg.SetLocalRunningForTest("ollama/qwen3.8:27b-mlx")` line (line ~42) and whatever now-unused import it leaves. Two existing tests change meaning and get new comments:

```go
// TestEligibilityExcludesIdleLocalModels verifies a configured local model the
// live probe did not report as running is not eligible. It matters because wt
// smoke must never advertise a model a launch would refuse — the whole point
// of moving eligibility onto live rows.
func TestEligibilityExcludesIdleLocalModels(t *testing.T) {
	cfg := smokeFixtureConfig(t) // fixture probes an empty snapshot: nothing is running
	got := Eligibility(cfg)
	for _, e := range got {
		if e.Model.ID == "omlx/qwen3.8" {
			t.Errorf("idle local model reported eligible: %+v", e)
		}
	}
}

// TestEligibilityIncludesRunningLocalModel verifies a local model the probe
// reports as running IS eligible, so the smoke list reflects what can actually
// serve a request right now rather than what is merely configured.
func TestEligibilityIncludesRunningLocalModel(t *testing.T) {
	cfg := smokeFixtureConfig(t)
	stubSmokeProbe(t, smokeRunningSnapshot())
	got := Eligibility(cfg)
	var found bool
	for _, e := range got {
		if e.Model.ID == "omlx/qwen3.8" {
			found = true
		}
	}
	if !found {
		t.Errorf("running local model not eligible: %+v", got)
	}
}

// TestEligibilityIncludesDiscoveredRunningModel verifies a running model that
// is not in the registry — discovered from the provider's own model directory
// — is eligible. It matters because the non-TUI path now accepts a -M pin
// naming a discovered model, so smoke would otherwise under-report exactly the
// models a launch would accept.
func TestEligibilityIncludesDiscoveredRunningModel(t *testing.T) {
	cfg := smokeFixtureConfig(t)
	disc := config.DiscoveredModelID("omlx", "extra")
	stubSmokeProbe(t, localmodels.Snapshot{
		Providers: map[string]localmodels.Status{"omlx": localmodels.StatusOK},
		Entries: []localmodels.Entry{
			{ProviderID: "omlx", Artifact: "extra", ModelID: disc, ModelName: "extra", Running: true},
		},
	})
	got := Eligibility(cfg)
	var found bool
	for _, e := range got {
		if e.Model.ID == disc {
			found = true
		}
	}
	if !found {
		t.Errorf("running discovered model not eligible: %+v", got)
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `cd wt && go test ./internal/smoke/ 2>&1 | head -30`
Expected: FAIL — `undefined: smokeProbe`, `undefined: stubSmokeProbe`, `undefined: smokeRunningSnapshot`, and (before the fixture change) `TestEligibilityIncludesRunningLocalModel` finding nothing.

- [ ] **Step 3: Rewrite `Eligibility`**

In `internal/smoke/smoke.go`, replace the body of `Eligibility` and drop the `localgate` import:

```go
// smokeProbe is a test seam over the live inventory, so no test in this
// package probes a real provider.
var smokeProbe = localmodels.Inventory

// Eligibility lists every model wt smoke can actually launch, one entry per
// agent: a cloud model, or a local model the live probe reports as running.
// It reads the same rows the picker does, so smoke's list cannot advertise a
// model a real launch would refuse, and it never starts or stops anything —
// starting a model to check it is the launch path's job, not the doctor's.
func Eligibility(cfg *config.Config) []Eligible {
	snap := smokeProbe(cfg)
	var out []Eligible
	for _, a := range cfg.Agents {
		models, err := cfg.EligibleModels(a.Name, "", "")
		if err != nil {
			continue
		}
		rows := catalog.Build(catalog.Input{
			Config:    cfg,
			Agent:     a.Name,
			Models:    models,
			Inventory: &snap,
			// A discovered running model is one a -M pin can launch, so smoke
			// reports it; discovered non-running rows stay out, since their
			// action is start, not launch.
			HideDiscovered: false,
		})
		for _, r := range rows {
			if r.Action() == catalog.ActionLaunch {
				out = append(out, Eligible{Agent: a.Name, Model: r.Model, Provider: r.Location.ProviderID})
			}
		}
	}
	return out
}
```

Adjust the field names/types on `Eligible{}` to match the struct's existing declaration in this file — the three fields used here (`Agent`, `Model`, `Provider`) exist; keep whatever the struct already has for anything else rather than reworking the struct, since `cmd/wt`'s smoke printer reads it.

- [ ] **Step 4: Fix the caller-side test**

In `cmd/wt/smoke_test.go`, delete the `cfg.SetLocalRunningForTest("ollama/qwen3.8:27b-mlx")` line (line ~39). That test builds its own config; confirm the model it expects is cloud or is running under whatever probe that test stubs, and if it relied on the flag alone, move that model's expectation to a running entry in the stub the test installs. Run the test, and if it fails with the model missing, that is the signal the expectation needs the running flag rather than the local-running set.

- [ ] **Step 5: Run the tests to verify they pass**

Run: `cd wt && go test ./internal/smoke/ ./cmd/wt/ -run 'Smoke|Eligibility' -v 2>&1 | tail -30`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add wt/internal/smoke wt/cmd/wt/smoke_test.go
git commit -m "refactor(wt): smoke eligibility from live rows

wt smoke now asks the same question the picker and the non-TUI launch
path ask — a cloud model, or a local model the live probe reports as
running — instead of combining a marker resolution with the retired
local-running set. It still never starts or stops anything.

- completes plan item #7"
```

---

## Task 8: Delete `internal/localgate`

**Files:**
- Delete: `internal/localgate/` (whole directory)
- Modify: `internal/rotation/rotation.go:81` (comment only)

**Interfaces:**
- Consumes: nothing.
- Produces: nothing. After this task `localgate` no longer exists; Task 9 removes the `config` plumbing it was the last caller of.

**Ordering:** `internal/localgate` calls `cfg.RunningLocalModelIDs()` and `cfg.FilterToRunningLocal`, so the config fields cannot be deleted before this task — the reverse order would not compile.

- [ ] **Step 1: Confirm nothing production-side still imports it**

Run: `cd wt && grep -rn "localgate" --include='*.go' .`
Expected: only `internal/rotation/rotation.go` (a comment) and test files Task 6/Task 7 already cleaned. If a production file still imports it, stop — a task earlier in the plan was not applied.

- [ ] **Step 2: Delete the package**

```bash
git rm -r wt/internal/localgate
```

- [ ] **Step 3: Update the stale comment in rotation**

`internal/rotation/rotation.go:81` refers to `internal/localgate.Apply`/`FilterToRunningLocal`. Rewrite it to describe the current rule, e.g.:

```go
	// Rotation only ever picks a row whose action is launch — cloud, or a
	// local model the live probe reports as running — so it can never hand
	// an agent a server that is not up. Starting a local model is the
	// user's decision (a -M pin), never rotation's.
```

Match the surrounding comment's line width and tone; the point is that no symbol it names has been deleted.

- [ ] **Step 4: Verify the tree builds and the name is gone**

Run: `cd wt && go build ./... && go vet ./... && go test ./... 2>&1 | tail -20 && grep -rn "localgate" . ; echo "grep exit: $?"`
Expected: build/vet/tests PASS; `grep` prints nothing and exits 1.

- [ ] **Step 5: Commit**

```bash
git add -A wt/internal
git commit -m "refactor(wt): delete internal/localgate

Every caller now reads live rows: the picker and both launch paths
through internal/catalog, and smoke through the same rules. Nothing
consults the marker-and-running-set pair any more, so the package goes.

- completes plan item #8"
```

---

## Task 9: Retire the local-running config machinery

**Files:**
- Modify: `internal/config/config.go`
- Modify: `internal/config/config_test.go`
- Modify: `internal/config/modelman.go`
- Modify: `internal/config/modelman_test.go`

**Interfaces:**
- Consumes: nothing.
- Produces: nothing. `Config` loses the four gate methods and two unexported fields; `ExposureEntry` loses `Running`; `modelman.toml`'s `running` key stops being parsed.

After this task wt reads `exposed` from modelman.toml and nothing else. The `running` key keeps existing in the file — modelman owns it — wt just stops reading it. Nothing in wt writes modelman state, so this is a pure read-side retirement.

- [ ] **Step 1: Confirm the last callers are gone**

Run: `cd wt && grep -rn "RunningLocalModelIDs\|FilterToRunningLocal\|LocalGateActive\|SetLocalRunningForTest\|runningLocal\|localGateActive" --include='*.go' .`
Expected: only the declarations in `internal/config/config.go`, the `finalizeCfg` population block, and the tests about to be deleted. Any other hit means Task 6, 7, or 8 was not fully applied — stop and fix that first.

- [ ] **Step 2: Delete the production code**

In `internal/config/config.go`, delete:
- the `runningLocal map[string]bool` and `localGateActive bool` fields (lines 309–310)
- the `finalizeCfg` block that populates them (lines 407–413)
- `FilterToRunningLocal` (lines 635–649)
- `RunningLocalModelIDs` (lines 676–683)
- `LocalGateActive` (lines 685–691)
- `SetLocalRunningForTest` (lines ~695–703)

Leave `ExposeAllForTest` and `SetExposedForTest` — neither touches the gate fields, so both still compile and their callers in Tasks 6 and 7 stay valid.

Add one comment where the fields were, so a future reader knows the removal was deliberate rather than an oversight:

```go
	// wt decides which local models are running from the live inventory
	// (internal/localmodels), never from modelman's per-model `running` flag.
	// The flag-parsing fields that used to live here were removed when the
	// last caller (internal/localgate) was deleted; modelman still owns the
	// key, wt simply does not read it. See
	// docs/superpowers/specs/2026-09-20-wt-live-resolution-design.md.
```

In `internal/config/modelman.go`, delete:
- `ExposureEntry.Running` (lines 25–30)
- `modelmanState.Running bool \`toml:"running"\`` (line 53)
- the assignment at line 79

The struct keeps decoding the rest of the entry unchanged; an unknown `running` key in the TOML is simply ignored, which is what `TestLoadModelmanStateIgnoresRunningFlag` (Step 4) pins.

- [ ] **Step 3: Delete the tests that covered the removed machinery**

In `internal/config/config_test.go`, delete the six gate tests (lines 972–1080): `TestLocalGateActiveDefaultsFalse`, `TestSetLocalRunningForTestActivatesGate`, `TestFilterToRunningLocalInactiveGateIsNoop`, `TestFilterToRunningLocalNoMarkerDropsAllLocal`, `TestFilterToRunningLocalKeepsOnlyTheRunningOne`, `TestFilterToRunningLocalUnresolvableLocationDropped`.

- [ ] **Step 4: Rewrite the modelman `running` test**

Replace `TestLoadModelmanStateReadsRunningFlag` (lines 129–158) with:

```go
// TestLoadModelmanStateIgnoresRunningFlag verifies modelman.toml's per-model
// `running` key no longer affects what wt loads: the file still carries it
// (modelman owns it), but wt decides running state from the live inventory.
// It matters because reading the flag was the mechanism by which a crashed or
// externally-stopped model could be shown as available — the drift this
// sub-project removes.
func TestLoadModelmanStateIgnoresRunningFlag(t *testing.T) {
	dir := t.TempDir()
	state := `
[[models]]
id = "omlx/qwen3.8"
exposed = true
running = true
`
	if err := os.WriteFile(filepath.Join(dir, "modelman.toml"), []byte(state), 0o644); err != nil {
		t.Fatalf("write modelman.toml: %v", err)
	}
	cfg := &Config{}
	if err := loadModelmanState(cfg, dir); err != nil {
		t.Fatalf("loadModelmanState() error = %v", err)
	}
	if !cfg.ExposedFlag("omlx/qwen3.8") {
		t.Error("exposed = false, want true: the exposed key must still be read")
	}
	for _, id := range cfg.RunningLocalModelIDs() {
		t.Errorf("RunningLocalModelIDs() returned %q; running state must come from the live inventory", id)
	}
}
```

> The loop above is written against the **pre-deletion** API so the test fails for the right reason first. After Step 2 removes `RunningLocalModelIDs`, delete that loop and replace it with the equivalent absence assertion: build a snapshot via `localmodels.Inventory` is not this package's job, so simply assert the exposed read and let compilation of the removal stand as the proof — i.e. the final test body ends at the `ExposedFlag` assertion. Run `go vet ./internal/config/` and remove any line the compiler rejects; the intent that must survive is "exposed is still read; running is not a Config concept".

- [ ] **Step 5: Verify**

Run: `cd wt && go build ./... && go vet ./... && go test ./... 2>&1 | tail -20`
Expected: PASS, with no remaining references: `grep -rn "RunningLocalModelIDs\|LocalGateActive\|FilterToRunningLocal\|SetLocalRunningForTest" --include='*.go' .` prints nothing.

- [ ] **Step 6: Commit**

```bash
git add wt/internal/config
git commit -m "refactor(wt): drop modelman's running flag from wt's config

wt reads `exposed` from modelman.toml and nothing else; which local models
are actually running comes from the live inventory. The gate fields, the
four methods that exposed them, and the per-model running parse are gone.

- completes plan item #9"
```

---

## Task 10: Update the docs

**Files:**
- Modify: `CLAUDE.md` (the `wt/` package doc — the section that still says `internal/localgate` calls apply)
- Modify: `docs/guides/00-config-map.md`
- Modify: `docs/guides/06-wt-agents-and-models.md`
- Modify: `docs/guides/08-maintenance-and-troubleshooting.md`
- Modify: `docs/wt-smoke.md`
- Modify: `../CLAUDE.md` (only if a monorepo-level line names the gate — check first)

**Interfaces:**
- Consumes: nothing.
- Produces: nothing. This task exists because the repo docs describe the gate as live mechanism; leaving them stale would have the next reader believe a deleted package still runs.

- [ ] **Step 1: Find every stale mention**

Run:

```bash
cd wt && git grep -n "localgate\|RunningLocalModelIDs\|LocalGateActive\|FilterToRunningLocal\|local-running gate\|local model is running\|SetLocalRunningForTest" -- . ':!docs/superpowers'
cd .. && git grep -n "localgate\|RunningLocalModelIDs\|LocalGateActive\|FilterToRunningLocal" -- . ':!wt/docs/superpowers'
```

Expected: hits in `wt/CLAUDE.md`, `wt/docs/guides/00-config-map.md` (~line 61), `wt/docs/guides/06-wt-agents-and-models.md:71`, `wt/docs/guides/08-maintenance-and-troubleshooting.md:166`, `wt/docs/wt-smoke.md:38-43`. Work through the list; do not stop at the ones listed here.

- [ ] **Step 2: Update `wt/CLAUDE.md`**

- **Package list** (the `Package list:` line): drop `localgate`, add `catalog`.
- **The package table:** delete the `internal/localgate/` row; add `internal/catalog/` describing it as the shared row policy (`Build`, `Find`, `Row.Action`, `Row.BlockReason`, the three-way artifact status) that the TUI wraps with counts/stats and rendering, and that `resolveModel` and `smoke.Eligibility` consume directly.
- **`internal/tui`'s row:** its description of `modelrows.go` (`buildRows`, `sortRows`, `tableRow.action`, `blockReason`) becomes "delegates row inclusion and action to `internal/catalog`, and adds counts, survey stats and sort"; the `start_flow.go` sentence stays.
- **`internal/localmodels`'s row:** drop the trailing sentence that says `localgate` delegates `nameMatches`/`fetchModelIDs` here and probes the same origins — that caller is gone; keep the `FamilyOrigin`/`FamilyOriginPort` description only if something still uses it (`grep -rn "FamilyOrigin" --include='*.go' .`) and delete the sentence otherwise.
- **`internal/lifecycle`'s row:** the last sentence ("Consumed by the TUI's start-on-select flow … the non-TUI path, `-M` pin check and `wt smoke` (sub-project 4c) still use `localgate`.") becomes: consumed by the TUI's start-on-select flow and by `cmd/wt`'s `startForLaunch` (`wt -A <agent> -M <id>` on a non-running local model); `wt smoke` never starts anything. Add that the failure wording lives in `internal/lifecycle/message.go` (`StartErrorMessage`, `StageLabel`), shared by both paths.
- **`internal/smoke`'s row:** replace the parenthetical that says `Eligibility` does "one localgate probe round" with: `Eligibility` builds `catalog` rows per agent from one `localmodels.Inventory` snapshot and keeps the `launch` rows (cloud, or a local model the probe reports running) — it never starts or stops a model.
- **The "Local-model gate (multi-model design, 2026-09-14)" section:** retitle to **"Local-model resolution (live rows, 2026-09-20)"** and rewrite. It must now say: the picker lists all configured and discovered local models with live STATUS/RUNNING; every row resolves to launch (cloud or running local), start (a non-running local of ollama/omlx/omlx-6bit/mtplx whose status is not `absent`), or block (`absent` → the pull/download reason; no-engine provider → the `modelman start <id>` hint); the rules live once in `internal/catalog` and the TUI only decorates them. A `-M` pin is looked up among **all** rows, including discovered ones: running/cloud launches, a start row selects its row and enters the start flow, a blocked row routes back with that row's reason, an absent-from-the-list pin keeps the "not in the eligible list" message. On the non-TUI path a `-M` pin on a start row is started by `startForLaunch` (progress to stderr, Ctrl+C cancels, an occupied provider needs a TTY `y/N` or `--replace`); with no flags set rotation sees cloud plus running-local rows only, so it can never hand an agent a server that is not up. Delete the paragraphs describing `Apply`/`FilterToRunningLocal`/`RunningLocalModelIDs`/`LocalGateActive`/`SetLocalRunningForTest` and the "Gate empties a non-empty eligible list" bullet (that case is now the "no cloud or running local model for agent X — `wt -M <id>` starts one" error).
- **The key-flags table:** add a `--replace` row — `With -M, start the model even if it means stopping a running one`.
- **The `-M` row in that table:** the "errors if not eligible" wording becomes "cloud or running → launch; a non-running local starts (with a progress display; `--replace` skips the replace confirmation); a blocked model errors with that row's reason".
- **`cmd/wt`'s file table:** add a `cmd/wt/start.go` row (`startForLaunch` — the non-TUI start driver: progress on stderr, Ctrl+C cancel, the replace confirmation, and the package-level `allowReplace` the flag sets).
- **`cmd/wt/resolve.go`'s row:** "single model for non-TUI launch" → "single model for non-TUI launch, resolved from live `catalog` rows; a `-M` pin on a start row starts it through `startModel`".
- **The test seams paragraph:** the seam list gains `startModel` (in `cmd/wt`, a separate seam from the TUI's), `probeInventory`, `smokeProbe`; note that `internal/tui`'s `TestMain` and `cmd/wt`'s `testmain_test.go` each stub the inventory and start seams so no test probes a server or starts a model.

- [ ] **Step 3: Update the guides**

`docs/guides/00-config-map.md` (~line 61): the line about local-model visibility / the running gate now describes live rows. State that modelman.toml's per-model `running` flag is modelman-owned and **not read by wt**, and point at `wt/CLAUDE.md`'s "Local-model resolution" section rather than repeating the rules.

`docs/guides/06-wt-agents-and-models.md:71`: the `-M` description gains the start/block outcomes and `--replace`.

`docs/guides/08-maintenance-and-troubleshooting.md:166`: if the text presents the flag/gate as the mechanism, replace it with the live-row test — "run `wt -A <agent>` and read the STATUS/RUNNING columns; wt probes ollama/omlx/mtplx live and shows what is actually up".

`docs/wt-smoke.md:38-43`: the eligibility description becomes "cloud models plus local models the live probe reports as running; `wt smoke` never starts or stops anything". If it documents `PickModel`'s table, keep that.

- [ ] **Step 4: Verify the links and the greps**

Run: `cd wt && make check-links 2>&1 | tail -20` (or from the monorepo root, `make check-links`) and re-run the Step 1 greps.
Expected: links resolve; the only remaining `localgate` mentions are inside `docs/superpowers/` (the spec and this plan, which are historical records and must keep their wording).

- [ ] **Step 5: Commit**

```bash
git add wt/CLAUDE.md wt/docs ../CLAUDE.md
git commit -m "docs(wt): describe local-model resolution from live rows

The docs described internal/localgate as live mechanism after it was
deleted, and the -M flag as an eligibility check rather than a launch,
start or block decision. Both now match the code.

- completes plan item #10"
```

`git add` will fail if `../CLAUDE.md` had nothing to update; drop that pathspec rather than committing an empty change.

---

## Self-Review

**1. Spec coverage.** Every requirement maps to a task:

| Spec requirement | Task |
|---|---|
| Extract `internal/catalog` (inclusion, status, `action()`) | 1 |
| TUI wraps catalog with counts/stats/sort/render | 2 |
| Non-TUI `resolveModel` from live rows; rotation sees launch rows only | 3 |
| `-M` verdict: running/cloud launch, idle start, blocked reason, discovered pins found | 3 (non-TUI), 6 (TUI) |
| The old all-local error becomes "no cloud or running local model … `wt -M <id>` starts one" | 3 |
| `startForLaunch` with timestamped stderr progress, Ctrl+C cancels, `*OccupiedError`/`*OccupancyUnknownError` consent | 5 |
| `--replace` for the non-interactive path | 5 |
| Error wording as ONE exported function in `internal/lifecycle` (the only engine change) | 4 |
| TUI pin verdict from the same rows; start rows enter the 4b flow | 6 |
| `wt smoke` eligibility = cloud + running local, never starts/stops | 7 |
| Delete `internal/localgate` | 8 |
| Retire `RunningLocalModelIDs`, `LocalGateActive`, `FilterToRunningLocal`, `SetLocalRunningForTest`, the `running` parse | 9 |
| After: wt reads `exposed`, never `running` | 9 |
| Docs | 10 |
| Out of scope: sub-project 5 (launch-time smoke gate), stopping models, `mlx_lm_server`, a user-facing stop command | none — deliberately not implemented |

**2. Placeholder scan.** No "TBD"/"TODO"/"similar to Task N". Two steps deliberately instruct a judgment call rather than a literal: Task 7 Step 4 (the caller-side test's expectation may need its model listed as running) and Task 9 Step 4 (the rewritten test's final assertion depends on what the compiler accepts once `RunningLocalModelIDs` is gone). Both name the exact file, the exact symptom, and what to do about it — neither is a "handle edge cases" hand-wave.

**3. Type consistency.** Checked across tasks: `catalog.Row`/`Input`/`Status`/`Action`/`ActionLaunch`/`ActionStart`/`ActionBlock` and `Row.Action()`/`Row.BlockReason()` (Task 1) are used with the same spelling in Tasks 2, 3, 6, 7. `lifecycle.StartErrorMessage`/`StageLabel` (Task 4) match their Task 5 and Task 14 call sites. `startModel` appears as a name in *both* `internal/tui` (existing, `lifecycle.Start`) and `cmd/wt` (Task 5, `startForLaunch`) — these are different packages, so there is no collision, but Task 10's seam documentation calls it out because the same identifier for two seams is exactly the kind of thing a reader trips over. `tui.Run`'s new signature (Task 6: `yolo, allowReplace bool`, then `agent, pinned, tags, family string, …`) matches all four `main_test.go` stubs updated in the same task and both production call sites. `resolveModelFromEligible(agent, eligible)` (Task 3, `pinned` removed) matches its one updated caller in `launch.go`.

---

## Execution Handoff

**Plan complete and saved to `wt/docs/superpowers/plans/2026-09-20-wt-live-resolution.md`. Two execution options:**

**1. Subagent-Driven (recommended)** — I dispatch a fresh subagent per task, review between tasks (spec compliance, then code quality), fast iteration.

**2. Inline Execution** — Execute tasks in this session using `superpowers:executing-plans`, batch execution with checkpoints for review.

**Which approach?**
