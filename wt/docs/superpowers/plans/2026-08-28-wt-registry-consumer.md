# wt as a Read-Only Registry Consumer — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make `wt` a read-only consumer of modelman-owned `registry.toml`: Providers/Models load from `~/.config/local-ai/registry.toml` (fail-closed if missing), `config.toml` keeps only Agents + DefaultTag, and wt deletes its live-discovery package and `wt config ollama`.

**Architecture:** `config.Config` keeps its in-memory shape (Providers, Models, Agents, DefaultTag) but is now a *joined view*: Agents/DefaultTag come from `config.toml`, Providers/Models from `registry.toml` (read-only, parsed by a new `internal/config/registry.go`). `Save` persists only wt-owned fields. The live-discovery `internal/registry` package, the `wt config ollama` command, and `internal/ollamaconfig` are deleted; `configeditor` shrinks to agents-only editing; `ollamacheck` runs `ollama list` inline instead of via the registry package.

**Tech Stack:** Go 1.x, BurntSushi/toml, cobra, Bubble Tea, stdlib `testing`.

**Spec:** `docs/superpowers/specs/2026-08-28-wt-registry-consumer-design.md`

**Note on the spec's `internal/catalog` package name:** the loader lives in
package `config` (new file `internal/config/registry.go`) instead of a separate
`internal/catalog` package. Reason: it must decode into `config.Provider`/`config.Model`,
and `config.Load()` must call it — a separate package would either duplicate the
types or create an import cycle. The spec's intent (small TOML-only loader, no
discovery) is unchanged.

---

## File Structure

- **Create: `internal/config/registry.go`** — `RegistryPath()` + `loadRegistry()` (TOML parse of modelman's registry.toml; no discovery).
- **Modify: `internal/config/config.go`** — `Save` trims Providers/Models; `Load` joins registry; doc comments.
- **Modify: `internal/config/migrate.go`** — `Migrate` writes the full config via a new `saveFull` so legacy models.conf data stays importable by `modelman migrate`.
- **Modify: `internal/ollamacheck/ollamacheck.go`** — inline `ollama list` (drop `internal/registry` import).
- **Delete: `internal/registry/`** (5 files), **`internal/ollamaconfig/`** (6 files).
- **Modify: `cmd/wt/commands_config.go`** — drop `configOllamaCmd`.
- **Modify: `internal/configeditor/`** — agents-only: rewrite `editor.go` + `delete.go`; delete `providers_tab.go`, `models_tab.go`, `provider_form.go`, `model_form.go` and their tests; trim `editor_test.go`/`delete_test.go`.
- **Modify: `cmd/wt/main_test.go`, `cmd/wt/commands_config_test.go`** — registry.toml fixtures.
- **Modify: `CLAUDE.md`, `README.md`**, and the cross-repo status tracker.
- **Create: `internal/config/registry_test.go`**; **modify `internal/configeditor/*_test.go`**, `internal/ollamacheck/ollamacheck_test.go`.

Files NOT touched: `internal/tui`, `internal/rotation`, `internal/agents`, `internal/config/config.go` selection helpers (`EligibleModels`, `ModelsForAgent`, `ModelsWithTag`), `cmd/wt/resolve.go`, `cmd/wt/launch.go`.

---

## Task 1: Decouple `ollamacheck` from `internal/registry`

`ollamacheck.Available` currently shells out via `registry.Ollama{}.Discover()`. Replace with a direct `ollama list` call and a local name parser so the `internal/registry` package can be deleted in Task 2.

**Files:**
- Modify: `internal/ollamacheck/ollamacheck.go`
- Test: `internal/ollamacheck/ollamacheck_test.go`

- [ ] **Step 1: Write the failing test**

Append to `internal/ollamacheck/ollamacheck_test.go`:

```go
func TestParseOllamaNames(t *testing.T) {
	out := "NAME              ID    SIZE      MODIFIED\n" +
		"gemma4:9b         abc   5.0 GB    2 days ago\n" +
		"kimi-k2.6:cloud   def   -         3 days ago\n"
	names := parseOllamaNames(out)
	if len(names) != 2 {
		t.Fatalf("parseOllamaNames: got %v, want 2 names", names)
	}
	if names[0] != "gemma4:9b" || names[1] != "kimi-k2.6:cloud" {
		t.Errorf("parseOllamaNames: got %v, want [gemma4:9b kimi-k2.6:cloud]", names)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/ollamacheck/ -run TestParseOllamaNames -v`
Expected: FAIL — `undefined: parseOllamaNames`

- [ ] **Step 3: Rewrite ollamacheck.go**

Replace the entire contents of `internal/ollamacheck/ollamacheck.go` with:

```go
// Package ollamacheck verifies whether an Ollama model is locally available.
package ollamacheck

import (
	"fmt"
	"os/exec"
	"strings"

	"github.com/ohanaverse/agent-worktree/internal/config"
)

// IsOllamaModel returns true if the model is from the ollama provider.
func IsOllamaModel(m config.Model) bool {
	return m.ProviderID == "ollama"
}

// Check reports whether m is locally available. Non-ollama models are always
// available (true, nil); ollama models are checked against `ollama list`.
func Check(m config.Model) (bool, error) {
	if !IsOllamaModel(m) {
		return true, nil
	}
	return Available(m.ModelName)
}

// Available checks whether modelName appears in `ollama list` output.
// Returns false with a nil error when ollama is not installed.
// Returns an error when `ollama list` exits non-zero.
//
// Deliberately self-contained (no internal/registry import): this is a
// runtime availability probe, not model discovery — modelman owns discovery.
func Available(modelName string) (bool, error) {
	if _, err := exec.LookPath("ollama"); err != nil {
		return false, nil // ollama not installed — nothing is available
	}
	out, err := exec.Command("ollama", "list").Output()
	if err != nil {
		return false, fmt.Errorf("ollama list: %w", err)
	}
	for _, name := range parseOllamaNames(string(out)) {
		if name == modelName {
			return true, nil
		}
	}
	return false, nil
}

// parseOllamaNames extracts the NAME column from `ollama list` output.
// Mirrors the row shape the old registry parser accepted: header line
// skipped, rows with fewer than 3 fields skipped, cloud rows (SIZE "-")
// included since a cloud model is "available" to ollama.
func parseOllamaNames(output string) []string {
	var names []string
	for i, line := range strings.Split(strings.TrimSpace(output), "\n") {
		if i == 0 {
			continue // header row: NAME  ID  SIZE  MODIFIED
		}
		fields := strings.Fields(line)
		if len(fields) < 3 || fields[0] == "" {
			continue
		}
		names = append(names, fields[0])
	}
	return names
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/ollamacheck/ -v`
Expected: PASS (new parse test + existing `TestAvailable` fake-binary test unchanged)

- [ ] **Step 5: Commit**

```bash
git add internal/ollamacheck/ollamacheck.go internal/ollamacheck/ollamacheck_test.go
git commit -m "refactor(ollamacheck): inline ollama list instead of registry discovery"
```

---

## Task 2: Remove `wt config ollama` and `internal/ollamaconfig`

`modelman sync` supersedes the ollama sync TUI.

**Files:**
- Delete: `internal/ollamaconfig/` (all 6 files)
- Modify: `cmd/wt/commands_config.go`

- [ ] **Step 1: Delete the package**

```bash
git rm -r internal/ollamaconfig
```

- [ ] **Step 2: Trim commands_config.go**

In `cmd/wt/commands_config.go`:

Remove the import line:

```go
	"github.com/ohanaverse/agent-worktree/internal/ollamaconfig"
```

In `configCmd`, remove `configOllamaCmd(a)` from the `AddCommand` line and the `ollama` line from the `Long` help text. The command becomes:

```go
func configCmd(a *app) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "config",
		Short: "Manage wt preferences and config.toml",
		Long: "Manage wt user preferences.\n\n" +
			"With no subcommand, launches an interactive TUI to view and edit\n" +
			"agents in config.toml.\n\n" +
			"Subcommands:\n" +
			"  theme   active color theme\n" +
			"  path    print the config directory",
		RunE: func(cmd *cobra.Command, args []string) error {
			return configeditorRun(a.theme, a.cfg, a.cfgErr)
		},
	}
	cmd.AddCommand(configPathCmd(a), configThemeCmd(a))
	return cmd
}
```

Delete the entire `configOllamaCmd` function (and its doc comment).

- [ ] **Step 3: Build and test**

Run: `go build ./... && go test ./cmd/wt/ -v`
Expected: BUILD OK, tests PASS (if `commands_config_test.go` asserts an `ollama` subcommand exists, delete that test function — grep first: `grep -n "ollama" cmd/wt/commands_config_test.go`)

- [ ] **Step 4: Commit**

```bash
git add -A
git commit -m "refactor: remove wt config ollama (superseded by modelman sync)"
```

---

## Task 3: Reduce `configeditor` to agents-only

Providers/models are modelman-owned now; wt's editor must stop offering to edit them.

**Files:**
- Delete: `internal/configeditor/providers_tab.go`, `models_tab.go`, `provider_form.go`, `model_form.go`, `providers_tab_test.go`, `models_tab_test.go`, `provider_form_test.go`, `model_form_test.go`
- Modify: `internal/configeditor/editor.go` (rewrite), `delete.go` (rewrite), `form_view.go` (comment)
- Test: `internal/configeditor/editor_test.go`, `delete_test.go`

- [ ] **Step 1: Delete provider/model sources and their tests**

```bash
git rm internal/configeditor/providers_tab.go internal/configeditor/models_tab.go \
      internal/configeditor/provider_form.go internal/configeditor/model_form.go \
      internal/configeditor/providers_tab_test.go internal/configeditor/models_tab_test.go \
      internal/configeditor/provider_form_test.go internal/configeditor/model_form_test.go
```

- [ ] **Step 2: Rewrite editor.go**

Replace the entire contents of `internal/configeditor/editor.go` with:

```go
// Package configeditor provides a TUI for viewing and editing the agent
// section of config.toml. Providers and models live in modelman-owned
// registry.toml and are read-only for wt, so the editor only manages agents.
package configeditor

import (
	"fmt"

	"github.com/charmbracelet/bubbles/list"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/ohanaverse/agent-worktree/internal/config"
	"github.com/ohanaverse/agent-worktree/internal/themes"
)

type phase int

const (
	phaseList phase = iota
	phaseForm
	phaseDelete
	phaseQuit
)

type formKind int

const (
	formNone formKind = iota
	formAgent
)

// loadedMsg carries the config after loading.
type loadedMsg struct {
	cfg *config.Config
	err error
}

type model struct {
	phase  phase
	theme  themes.Theme
	cfg    *config.Config
	dirty  bool
	width  int
	height int
	ready  bool
	status string // shown above the list
	saving bool   // prevents duplicate save dispatches
	cfgErr error  // captured at construction for the initial loadedMsg

	list list.Model

	// delete state
	deleteTarget deleteTarget
	deleteError  string

	// quit state
	quitting bool // true when waiting for save-before-quit

	// Form state
	formKind   formKind
	formIsNew  bool
	formCursor int
	formError  string

	// Agent form fields
	agEdit                 config.Agent
	agName                 textinput.Model
	agProvidersInput       textinput.Model // comma-separated provider IDs
	agDefaultProviderInput textinput.Model
	agInstalled            bool // cached to avoid PATH lookup per frame
}

func newModel(theme themes.Theme, cfg *config.Config, cfgErr error) model {
	return model{theme: theme, cfg: cfg, cfgErr: cfgErr}
}

// Init emits the loaded config immediately. The config is supplied by the
// caller (cmd/wt) rather than reloaded inside the TUI, so a validation error
// can be surfaced without hanging on the "Loading config..." screen.
func (m model) Init() tea.Cmd {
	return func() tea.Msg {
		// cfg and cfgErr are captured when newModel is called.
		return loadedMsg{cfg: m.cfg, err: m.cfgErr}
	}
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		if m.ready {
			m.list.SetSize(msg.Width-2, msg.Height-4)
		}
		if m.phase == phaseForm {
			m.resizeFormInputs()
		}
	case loadedMsg:
		m.ready = true
		if msg.cfg == nil {
			msg.cfg = &config.Config{DefaultTag: "code"}
		}
		m.cfg = msg.cfg
		if msg.err != nil {
			m.status = "config load/validation error: " + msg.err.Error()
		}
		m.list = buildAgentsList(m.theme, m.width-2, m.height-4, m.cfg)
		return m, nil
	case saveMsg:
		m.saving = false
		if msg.err != nil {
			m.status = "save failed: " + msg.err.Error()
			m.quitting = false
			return m, nil
		}
		m.dirty = false
		m.status = "saved"
		if m.quitting {
			m.quitting = false
			return m, tea.Quit
		}
		return m, nil
	}

	if m.phase == phaseForm {
		return m.handleFormUpdate(msg)
	}
	if m.phase == phaseDelete {
		return m.handleDeleteUpdate(msg)
	}
	if m.phase == phaseQuit {
		return m.handleQuitUpdate(msg)
	}

	if msg, ok := msg.(tea.KeyMsg); ok {
		// Delegate to the list while it is filtering so single-key global
		// shortcuts (n, d, q) don't intercept filter input.
		if m.ready && m.list.FilterState() == list.Filtering {
			var cmd tea.Cmd
			m.list, cmd = m.list.Update(msg)
			return m, cmd
		}

		// Global shortcuts take precedence over list delegation.
		switch msg.String() {
		case "ctrl+s":
			return m.handleSave()
		case "q", "ctrl+c":
			if !m.dirty {
				return m, tea.Quit
			}
			m.phase = phaseQuit
			m.quitting = false
			return m, nil
		case "d":
			if m.ready {
				if it, ok := m.list.SelectedItem().(agentItem); ok {
					if !it.command && it.configured {
						enterDelete(&m, it.agent.Name)
					}
				}
			}
			return m, nil
		case "n":
			if m.ready {
				enterAgentForm(&m, config.Agent{}, true)
			}
			return m, nil
		case "enter":
			if m.ready {
				if it, ok := m.list.SelectedItem().(agentItem); ok {
					if it.command {
						return m, nil
					}
					enterAgentForm(&m, it.agent, !it.configured)
				}
			}
			return m, nil
		}

		// Not a global shortcut — delegate to the list.
		if m.ready {
			var cmd tea.Cmd
			m.list, cmd = m.list.Update(msg)
			if cmd != nil {
				return m, cmd
			}
		}
	}
	return m, nil
}

// handleQuitUpdate processes keys in the unsaved-changes quit prompt.
func (m model) handleQuitUpdate(msg tea.Msg) (tea.Model, tea.Cmd) {
	if msg, ok := msg.(tea.KeyMsg); ok {
		switch msg.String() {
		case "y", "Y":
			m.quitting = true
			return m.handleSave()
		case "n", "N":
			return m, tea.Quit
		case "c", "C", "esc":
			m.phase = phaseList
			m.quitting = false
			return m, nil
		}
	}
	return m, nil
}

func (m model) View() string {
	if !m.ready {
		return "Loading config..."
	}
	switch m.phase {
	case phaseForm:
		return m.formView()
	case phaseDelete:
		return m.deleteView()
	case phaseQuit:
		return "You have unsaved changes. Save before quitting?\n\n[y] save and quit  [n] discard and quit  [c] cancel\n"
	default:
		var status string
		if m.status != "" {
			status = m.status + "\n\n"
		}
		return "Agents (providers/models are managed by modelman)\n\n" + status + m.list.View()
	}
}

// resizeFormInputs updates input widths after a resize.
func (m *model) resizeFormInputs() {
	w := m.width - 20
	if w < 10 {
		w = 10
	}
	m.agName.Width = w
	m.agProvidersInput.Width = w
	m.agDefaultProviderInput.Width = w
}

// formView dispatches to the appropriate form renderer.
func (m model) formView() string {
	if m.formKind == formAgent {
		return m.agentFormView()
	}
	return ""
}

// handleFormUpdate dispatches to the appropriate form update handler.
func (m model) handleFormUpdate(msg tea.Msg) (tea.Model, tea.Cmd) {
	if m.formKind == formAgent {
		return m.handleAgentFormUpdate(msg)
	}
	return m, nil
}

// Run starts the config editor TUI with the config already loaded by the
// caller. A non-nil cfgErr is surfaced as a status message so the user can
// repair the config without the CLI exiting early.
func Run(theme themes.Theme, cfg *config.Config, cfgErr error, opts ...tea.ProgramOption) error {
	m := newModel(theme, cfg, cfgErr)
	allOpts := append([]tea.ProgramOption{tea.WithAltScreen()}, opts...)
	p := tea.NewProgram(m, allOpts...)
	_, err := p.Run()
	return err
}
```

- [ ] **Step 3: Rewrite delete.go**

Replace the entire contents of `internal/configeditor/delete.go` with:

```go
package configeditor

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
)

// deleteTarget identifies the agent pending deletion.
type deleteTarget struct {
	id string
}

// enterDelete transitions to the delete confirmation phase.
func enterDelete(m *model, id string) {
	m.phase = phaseDelete
	m.deleteTarget = deleteTarget{id: id}
	m.deleteError = ""
}

// handleDeleteUpdate processes keys in the delete confirmation prompt.
func (m model) handleDeleteUpdate(msg tea.Msg) (tea.Model, tea.Cmd) {
	if msg, ok := msg.(tea.KeyMsg); ok {
		switch msg.String() {
		case "y", "Y":
			return m.confirmDelete()
		case "n", "N", "esc":
			m.phase = phaseList
			m.deleteTarget = deleteTarget{id: ""}
			return m, nil
		}
	}
	return m, nil
}

// confirmDelete removes the agent and rebuilds the list.
func (m model) confirmDelete() (tea.Model, tea.Cmd) {
	if m.deleteTarget.id == "" {
		m.phase = phaseList
		return m, nil
	}

	m.cfg.DeleteAgent(m.deleteTarget.id)

	m.dirty = true
	m.phase = phaseList
	m.deleteTarget = deleteTarget{id: ""}
	m.list = buildAgentsList(m.theme, m.width-2, m.height-4, m.cfg)
	return m, nil
}

// deleteView renders the delete confirmation prompt.
func (m model) deleteView() string {
	var b strings.Builder
	b.WriteString(fmt.Sprintf("Delete agent %q? [y/N]\n", m.deleteTarget.id))
	if m.deleteError != "" {
		b.WriteString("\n" + m.deleteError + "\n")
	}
	return b.String()
}
```

- [ ] **Step 4: Update form_view.go doc comment**

In `internal/configeditor/form_view.go`, replace the comment on `renderFormFields`:

```go
// renderFormFields renders a list of labeled form fields. The focused row is
// highlighted with the theme's accent color.
```

- [ ] **Step 5: Trim the tests**

In `internal/configeditor/editor_test.go`:
- Delete `TestTab_Cycles`, `TestTab_NumberKeys_Jump`, and `TestSwitchSection_RebuildsList` (sections no longer exist).
- In `TestLoadedMsg_BuildsLists`, `TestNewKey_OpensAddForm`, and `TestEnterKey_OpensEditFormForSelectedItem`: delete the `sectionProviders`/`sectionModels` cases and any `m.lists[...]`/`m.section` references, keeping only the agent cases. The list field is now `m.list` (not `m.lists[sectionAgents]`).

In `internal/configeditor/delete_test.go`:
- Delete `TestDelete_Provider_BlockedByModelRef`, `TestDelete_Provider_BlockedByAgentRef`, and `TestDelete_Model_Succeeds`.
- In the remaining tests, replace `enterDelete(&m, sectionAgents, ...)` with `enterDelete(&m, ...)` (single-argument form) and `m.lists[sectionAgents]` with `m.list`.

In `internal/configeditor/quit_test.go`: replace `m.lists[...]`/`m.section` references with `m.list` (2 occurrences).

- [ ] **Step 6: Build and test**

Run: `go build ./... && go test ./internal/configeditor/ -v`
Expected: BUILD OK, remaining tests PASS

- [ ] **Step 7: Commit**

```bash
git add -A
git commit -m "refactor(configeditor): agents-only editing; providers/models are modelman-owned"
```

---

## Task 4: Load Providers/Models from `registry.toml`; Save writes wt-owned fields only

**Files:**
- Create: `internal/config/registry.go`
- Modify: `internal/config/config.go` (Config docs, `Load`, `Save`)
- Modify: `internal/config/migrate.go` (`saveFull`)
- Test: `internal/config/registry_test.go` (new), `cmd/wt/main_test.go`, `cmd/wt/commands_config_test.go`, `internal/configeditor/save_test.go`

- [ ] **Step 1: Write the failing tests**

Create `internal/config/registry_test.go`:

```go
package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeRegistry writes a registry.toml under dir/local-ai/ and points
// XDG_CONFIG_HOME at dir.
func writeRegistry(t *testing.T, dir, content string) {
	t.Helper()
	regDir := filepath.Join(dir, "local-ai")
	if err := os.MkdirAll(regDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(regDir, "registry.toml"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XDG_CONFIG_HOME", dir)
}

const minimalRegistry = `
[[providers]]
id = "ollama"
name = "Ollama"
location = "local"
auth = { type = "none", base_url = "http://localhost:11434" }

[[models]]
id = "ollama/gemma4:9b"
family = "gemma4"
provider_id = "ollama"
model_name = "gemma4:9b"
location = "local"
tags = ["code"]
`

func TestRegistryPathHonorsXDG(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "/custom/xdg")
	if got := RegistryPath(); !strings.HasSuffix(got, "/local-ai/registry.toml") && !strings.HasSuffix(got, "\\local-ai\\registry.toml") {
		t.Errorf("RegistryPath() = %q, want suffix local-ai/registry.toml", got)
	}
}

func TestLoad_JoinsRegistry(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	writeRegistry(t, dir, minimalRegistry)
	if err := os.MkdirAll(Dir(), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(Path(), []byte("default_tag = \"code\"\n[[agents]]\nname = \"claude\"\nsupported_providers = [\"ollama\"]\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(cfg.Providers) != 1 || cfg.Providers[0].ID != "ollama" {
		t.Errorf("providers = %+v, want one ollama provider", cfg.Providers)
	}
	if len(cfg.Models) != 1 || cfg.Models[0].ID != "ollama/gemma4:9b" {
		t.Errorf("models = %+v, want one gemma4 model", cfg.Models)
	}
}

func TestLoad_FailsClosedWithoutRegistry(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	_, err := Load()
	if err == nil {
		t.Fatal("expected error when registry.toml is missing")
	}
	if !strings.Contains(err.Error(), "modelman migrate") {
		t.Errorf("error should point at `modelman migrate`, got: %v", err)
	}
}

func TestLoad_RegistryExtraFieldsIgnored(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	writeRegistry(t, dir, `
[[providers]]
id = "ollama"
name = "Ollama"
model_dir = "/extra/ignored"
[models.model_info]
supports_function_calling = true

[[models]]
id = "ollama/gemma4:9b"
family = "gemma4"
provider_id = "ollama"
model_name = "gemma4:9b"
location = "local"
tags = ["code"]
[cost]
kind = "free"
`)
	if err := os.MkdirAll(Dir(), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(Path(), []byte("default_tag = \"code\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(cfg.Models) != 1 || cfg.Models[0].ModelName != "gemma4:9b" {
		t.Errorf("registry model not loaded: %+v", cfg.Models)
	}
}

func TestSave_OmitsProvidersAndModels(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	cfg := &Config{
		DefaultTag: "code",
		Providers:  []Provider{{ID: "ollama", Name: "Ollama"}},
		Models:     []Model{{ID: "ollama/x", ProviderID: "ollama", ModelName: "x"}},
		Agents:     []Agent{{Name: "claude", SupportedProviders: []string{"ollama"}}},
	}
	if err := Save(cfg); err != nil {
		t.Fatalf("Save: %v", err)
	}
	data, err := os.ReadFile(Path())
	if err != nil {
		t.Fatal(err)
	}
	s := string(data)
	if strings.Contains(s, "[[providers]]") || strings.Contains(s, "[[models]]") {
		t.Errorf("Save must not persist providers/models (modelman owns registry.toml):\n%s", s)
	}
	if !strings.Contains(s, "[[agents]]") {
		t.Errorf("Save must persist agents:\n%s", s)
	}
}

func TestLoad_LegacyConfigSectionsIgnored(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	writeRegistry(t, dir, minimalRegistry)
	if err := os.MkdirAll(Dir(), 0o755); err != nil {
		t.Fatal(err)
	}
	// A pre-Phase-4 config.toml still carries providers/models; they must be
	// ignored (registry.toml is the source of truth) and never resurrected.
	legacy := "default_tag = \"code\"\n[[providers]]\nid = \"stale\"\nname = \"Stale\"\n[[models]]\nid = \"stale/x\"\nprovider_id = \"stale\"\nmodel_name = \"x\"\n"
	if err := os.WriteFile(Path(), []byte(legacy), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	for _, p := range cfg.Providers {
		if p.ID == "stale" {
			t.Error("legacy config.toml provider leaked into the joined catalog")
		}
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/config/ -run 'TestLoad_JoinsRegistry|TestSave_Omits|TestRegistryPath' -v`
Expected: FAIL — `undefined: RegistryPath`

- [ ] **Step 3: Create `internal/config/registry.go`**

```go
package config

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/BurntSushi/toml"
)

// RegistryPath returns the modelman-owned registry.toml location. It honors
// XDG_CONFIG_HOME the same way Dir() does; modelman writes the registry to
// ~/.config/local-ai/registry.toml by default. wt reads this file read-only.
func RegistryPath() string {
	base := os.Getenv("XDG_CONFIG_HOME")
	if base == "" {
		home, _ := os.UserHomeDir()
		base = home
		if base != "" {
			base = filepath.Join(base, ".config")
		}
	}
	return filepath.Join(base, "local-ai", "registry.toml")
}

// loadRegistry decodes modelman-owned registry.toml into providers and
// models. Registry fields wt doesn't consume (cost, model_info, fetch,
// model_dir, auth secret_ref/base_url) are ignored by the decoder.
//
// Fail-closed: a missing or malformed registry is an error — wt has no
// editor for this file; seed it once with `modelman migrate`.
func loadRegistry() ([]Provider, []Model, error) {
	path := RegistryPath()
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil, nil, fmt.Errorf(
			"model registry not found at %s — seed it with `modelman migrate`", path)
	}
	if err != nil {
		return nil, nil, err
	}
	var reg struct {
		Providers []Provider `toml:"providers"`
		Models    []Model    `toml:"models"`
	}
	if _, err := toml.Decode(string(data), &reg); err != nil {
		return nil, nil, fmt.Errorf("parse %s: %w", path, err)
	}
	return reg.Providers, reg.Models, nil
}
```

- [ ] **Step 4: Rewire `Load` and trim `Save` in config.go**

In `internal/config/config.go`, replace `Load` with:

```go
// Load reads config.toml (Agents + DefaultTag — wt-owned) and joins it with
// modelman-owned registry.toml (Providers + Models) into one in-memory
// Config. The registry is checked before any schema-migration save so a
// missing registry fails closed before wt rewrites config.toml: legacy
// provider/model sections must survive on disk for `modelman migrate` to
// import them. Returns an empty Config if config.toml does not exist yet.
func Load() (*Config, error) {
	if _, err := Migrate(); err != nil {
		return nil, fmt.Errorf("migration: %w", err)
	}

	cfg := &Config{DefaultTag: "code"}
	data, err := os.ReadFile(Path())
	if os.IsNotExist(err) {
		providers, models, regErr := loadRegistry()
		if regErr != nil {
			return nil, regErr
		}
		cfg.Providers, cfg.Models = providers, models
		return cfg, nil
	}
	if err != nil {
		return nil, err
	}
	if _, err := toml.Decode(string(data), cfg); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}

	providers, models, err := loadRegistry()
	if err != nil {
		return nil, err
	}

	changed, err := migrateConfigSchema(cfg)
	if err != nil {
		return nil, fmt.Errorf("schema migration: %w", err)
	}
	if changed {
		if err := Save(cfg); err != nil {
			return nil, fmt.Errorf("save migrated config: %w", err)
		}
		fmt.Fprintln(os.Stderr, "wt: migrated config to native-provider alignment (renamed google→agy, removed opencode native)")
	}

	// Join modelman-owned registry LAST so wt never mutates registry data
	// in memory (schema fixups above only ever see wt-owned config.toml
	// content). Providers/Models from a pre-Phase-4 config.toml are
	// overwritten here — registry.toml is the source of truth.
	cfg.Providers, cfg.Models = providers, models
	return cfg, nil
}
```

Replace `Save` with:

```go
// Save writes cfg to the config path using an atomic temp-file + rename.
// Only wt-owned fields are persisted: Providers/Models live in
// modelman-owned registry.toml and are never written by wt.
func Save(cfg *Config) error {
	trimmed := *cfg
	trimmed.Providers = nil
	trimmed.Models = nil
	var buf bytes.Buffer
	if err := toml.NewEncoder(&buf).Encode(&trimmed); err != nil {
		return err
	}
	return WriteFileAtomic(Path(), buf.Bytes(), 0o644)
}
```

Update the `Config` struct doc comment:

```go
// Config is wt's in-memory configuration: Agents + DefaultTag come from
// wt-owned config.toml; Providers + Models come from modelman-owned
// registry.toml and are never persisted by wt (see Save).
```

- [ ] **Step 5: Preserve legacy data in `Migrate` (migrate.go)**

In `internal/config/migrate.go`, add next to the other write helpers (near `WriteFileAtomic` usage) and switch `Migrate`'s final save:

```go
// saveFull writes the complete cfg — including Providers/Models — to the
// config path. Only the legacy models.conf migration uses it: the
// provider/model sections it seeds must survive on disk so `modelman
// migrate` can import them into registry.toml. Regular saves use Save,
// which persists wt-owned fields only.
func saveFull(cfg *Config) error {
	var buf bytes.Buffer
	if err := toml.NewEncoder(&buf).Encode(cfg); err != nil {
		return err
	}
	return WriteFileAtomic(Path(), buf.Bytes(), 0o644)
}
```

In `func Migrate()`, replace the final `if err := Save(cfg); err != nil {` with:

```go
	if err := saveFull(cfg); err != nil {
```

- [ ] **Step 6: Update fixtures in cmd/wt tests**

In `cmd/wt/commands_config_test.go`, extend `newTestApp` to write a registry.toml (Load now fail-closes without one):

```go
func newTestApp(t *testing.T) (*app, string) {
	t.Helper()
	tmp := t.TempDir()
	origDirFunc := themes.DirFuncForTest()
	themes.SetDirFuncForTest(func() string { return tmp })
	t.Cleanup(func() { themes.SetDirFuncForTest(origDirFunc) })
	if err := os.WriteFile(filepath.Join(tmp, "config.toml"),
		[]byte("default_tag = \"code\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Load fail-closes without modelman-owned registry.toml.
	regDir := filepath.Join(tmp, "local-ai")
	if err := os.MkdirAll(regDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(regDir, "registry.toml"),
		[]byte("providers = []\nmodels = []\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	a, err := newApp()
	if err != nil {
		t.Fatalf("newApp() error = %v", err)
	}
	return a, tmp
}
```

In `cmd/wt/main_test.go` `TestAgentWithOneEligibleModelAutoLaunches`: keep the `[[agents]]` block in config.toml but move the `[[providers]]`/`[[models]]` sections into a registry file. Replace the config-writing block with:

```go
	cfgDir := filepath.Join(home, ".config", "agent-wt")
	if err := os.MkdirAll(cfgDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(cfgDir, "config.toml"),
		[]byte("default_tag = \"code\"\n[[agents]]\nname = \"claude\"\nsupported_providers = [\"ollama\"]\ndefault_provider = \"ollama\"\n"),
		0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	// Providers/models now live in modelman-owned registry.toml.
	regDir := filepath.Join(home, ".config", "local-ai")
	if err := os.MkdirAll(regDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(regDir, "registry.toml"),
		[]byte("providers = []\n[[models]]\nid = \"ollama/gemma4:9b\"\nprovider_id = \"ollama\"\nmodel_name = \"gemma4:9b\"\nlocation = \"local\"\ntags = [\"code\"]\n"),
		0o644); err != nil {
		t.Fatalf("write registry: %v", err)
	}
```

(In `internal/configeditor/save_test.go`, `TestSave_AtomicWrite` asserts the saved file contains "ollama" from a provider — with trimmed Save that assertion is now false. Change the seeded config to carry the provider reference via an agent and assert on the agent name instead: seed `Agents: []config.Agent{{Name: "claude", SupportedProviders: []string{"ollama"}}}` and assert `strings.Contains(string(data), "claude")`.)

- [ ] **Step 7: Run the full config + cmd test suites**

Run: `go test ./internal/config/ ./cmd/wt/ ./internal/configeditor/ -v`
Expected: PASS

- [ ] **Step 8: Commit**

```bash
git add -A
git commit -m "feat(config): join modelman-owned registry.toml; save writes wt-owned fields only"
```

---

## Task 5: Delete orphaned provider/model mutation methods

With provider/model editing gone, these `config` methods lose their last consumers: `UpsertProvider`, `DeleteProvider`, `ProviderInUse`, `UpsertModel`, `DeleteModel`, `ModelsByFamily`, `ModelsByProvider`, `ProvidersForAgent`.

**Files:**
- Modify: `internal/config/config.go`
- Test: `internal/config/mutation_test.go`, `internal/config/config_test.go`

- [ ] **Step 1: Verify nothing outside config still calls them**

Run: `grep -rn "UpsertProvider\|DeleteProvider\|UpsertModel\|DeleteModel\|ProviderInUse\|ModelsByFamily\|ModelsByProvider\|ProvidersForAgent" cmd/ internal/ --include="*.go" | grep -v "internal/config/"`
Expected: no output. If something appears, stop and reassess before deleting.

- [ ] **Step 2: Delete the methods**

From `internal/config/config.go` remove: `ProviderInUse`, `UpsertProvider`, `DeleteProvider`, `UpsertModel`, `DeleteModel`, `ModelsByFamily`, `ModelsByProvider`, `ProvidersForAgent`.

Keep: `ModelsWithTag` (used by `wt rotate`), `ProviderByID`, `ResolveLocation`, `ModelsForAgent`, `ModelsForAgentAndTag`, `EligibleModels`, all Agent methods.

- [ ] **Step 3: Trim the tests**

In `internal/config/mutation_test.go` delete the test functions covering the removed methods (grep `^func Test` and remove those referencing `UpsertProvider`, `DeleteProvider`, `UpsertModel`, `DeleteModel`, `ProviderInUse`, `ModelsByFamily`, `ModelsByProvider`, `ProvidersForAgent`); keep the `UpsertAgent`/`DeleteAgent` tests.

- [ ] **Step 4: Run tests**

Run: `go build ./... && go test ./...`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add -A
git commit -m "refactor(config): drop provider/model mutation methods (wt is a read-only consumer)"
```

---

## Task 6: Update docs and the cross-repo status tracker

**Files:**
- Modify: `CLAUDE.md`, `README.md`, `docs/superpowers/plans/2026-08-27-model-management-consolidation-status.md`

- [ ] **Step 1: CLAUDE.md**

- Line 81 package list: drop `registry` and `ollamaconfig` from `internal/{...}`.
- Package table: delete the `internal/registry/` and `internal/ollamaconfig/` rows; update the `internal/config/` row to "config load/validate/save (agents + joined registry catalog); helpers (`Dir`, `WriteFileAtomic`, `OllamaBaseURL`, `FirstTag`)".
- Replace the "## Live discovery" section with:

```markdown
## Registry (modelman-owned)

`~/.config/local-ai/registry.toml` holds the canonical Providers/Models.
wt loads it read-only via `config.Load` (fail-closed: missing/malformed
registry is an error; seed with `modelman migrate`) and joins it in memory
with its own `config.toml`, which now holds only Agents + DefaultTag. `Save`
persists Agents + DefaultTag only — wt never writes providers/models. Extra
registry fields (cost, model_info, fetch, model_dir, auth secret_ref/base_url)
are ignored by wt's parser.

**Lazy:** `newApp()` only loads config. wt never shells out for discovery;
`-W`/`--cwd` runs one `ollama list` via `ollamacheck.Available()`.
```

- Command list: delete the `wt config ollama` line.
- Update the `wt config` line's comment if it mentions providers/models.

- [ ] **Step 2: README.md**

- Package table: delete the `internal/registry/` row; reword `internal/config/` to "Config loading, model registry types (joined from modelman's registry.toml), validation, secrets, legacy migration".
- Config section ("three entity types"): describe the split — `config.toml` holds agents; Providers/Models come from `~/.config/local-ai/registry.toml` (modelman-owned, seeded with `modelman migrate`).
- Remove/adjust the legacy `models.conf` migration paragraph if it claims models land in `config.toml` (they now land in `registry.toml` via `modelman migrate`).

- [ ] **Step 3: Status tracker**

In `docs/superpowers/plans/2026-08-27-model-management-consolidation-status.md`, change the Phase 4 paragraph under Sub-project 1 to reflect completion (mark Phase 4 merged with this PR number, and note sub-projects 2/3 remain unbrainstormed).

- [ ] **Step 4: Commit**

```bash
git add -A
git commit -m "docs: wt reads modelman-owned registry.toml; wt config ollama removed"
```

---

## Final verification

- [ ] `go build ./...`
- [ ] `go vet ./...`
- [ ] `go test ./...`
- [ ] `gofmt -l .` (empty output)
- [ ] Smoke test with a real registry: `wt config path` and `wt --version` succeed; with `~/.config/local-ai/registry.toml` present, `wt -A claude` reaches the model picker (or rename the binary for a dry run — do not launch a real agent).
- [ ] Confirm `wt config ollama` is gone: `wt config ollama` prints an unknown-command error.

---

## Self-review notes

- **Spec coverage:** joined in-memory Config + read-only registry (Task 4), fail-closed missing registry (Task 4, `loadRegistry`), Save writes wt-owned fields only (Task 4), validation against joined catalog (unchanged `Validate` now sees registry data), configeditor agents-only (Task 3), `internal/registry` + `wt config ollama` deletion (Tasks 1-2), ollamacheck decoupled (Task 1), docs + tracker (Task 6). All spec sections map to a task.
- **Type consistency:** `loadRegistry()` returns `([]Provider, []Model, error)` and is consumed only by `Load`; `enterDelete` drops its section parameter and both call sites (editor.go `d` handler, delete.go) use the new single-arg form; `model.list` replaces `model.lists [3]` everywhere.
- **Deliberate ordering:** ollamacheck decoupled (T1) before `internal/registry` deletion (T2); editor reduced (T3) before Save stops persisting providers/models (T4) so no intermediate state silently drops editor writes.
- **Known accepted edge:** a machine that never created `config.toml` (still on legacy `models.conf`) hits the fail-closed registry error before the legacy migration can bootstrap; `models.conf` remains on disk and the error points at `modelman migrate`.
- **No placeholders:** every code step contains complete code; commands state expected results.