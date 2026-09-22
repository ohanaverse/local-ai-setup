# wt-owned LiteLLM management Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Move LiteLLM management (the `config.yaml` model_list sync, proxy restart, and `[litellm]` routing state) from modelman (Python) into wt (Go), so `wt start`/`wt stop` keep LiteLLM routes correct and modelman calls wt primitives.

**Architecture:** A new `wt/internal/litellm` package is the sole implementation (policy table, row builder, comment-preserving YAML editor, restart, reconcile). It is exposed as `wt litellm ...` subcommands and called directly from `internal/lifecycle`'s public `Start`/`Stop`/`StopModel` (which every start/stop path — non-TUI, TUI, `wt smoke`, stop picker — already goes through). modelman's Python write path is deleted and replaced by a subprocess bridge to `wt litellm`.

**Tech Stack:** Go 1.26 (`gopkg.in/yaml.v3`, `BurntSushi/toml`, cobra), Python 3.14/uv (modelman, typer, pytest).

**Spec:** `docs/superpowers/specs/2026-09-21-wt-litellm-ownership-design.md`

## Global Constraints

- **Ownership:** wt writes `~/.config/litellm/config.yaml` and its own `~/.config/agent-wt/config.toml`. wt NEVER writes `modelman.toml` or `registry.toml`.
- **Local-model routes:** `config.yaml` `model_list` membership is the source of truth; wt never writes the `exposed` flag.
- **Ready gate:** the lifecycle hook passes `SkipReadyGate: true` (the model is verifiably running); the `wt litellm expose` CLI enforces the gate unless `--skip-ready-gate` is passed (modelman passes it — it has already applied the gate against its in-memory state).
- **Native providers are never routed;** unmapped providers are rejected.
- **Restart never fails a caller:** a failed restart returns warnings only. A LiteLLM failure during `wt start`/`wt stop` prints a warning and never fails the start/stop. A missing `config.yaml` (`litellm.ErrMissing`) is silent in the lifecycle hook (LiteLLM not set up).
- **Env vars:** config path `WT_LITELLM_CONFIG` → fallback `MODELMAN_LITELLM_CONFIG` → `~/.config/litellm/config.yaml`; restart command `WT_LITELLM_RESTART_CMD` → fallback `MODELMAN_LITELLM_RESTART_CMD` → `launchctl kickstart -k gui/$(id -u)/local.litellm.proxy`.
- **Tests:** every test carries a comment stating what it covers and why it matters (user rule). No test may run the real restart command, real `launchctl`, or the real `wt` binary.
- **Commits:** reference the plan item ("— completes plan item #N") and end with `Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>`. Never push or open a PR without asking the user; each phase is one PR.
- **Worktree:** execute in a linked worktree on a feature branch; never check out `main` in a worktree.
- **Docs drift:** `git grep -n "exposed = " docs/guides/` before and after touching modelman state (root `CLAUDE.md`).
- **Go quality gate per task:** `cd wt && go build ./... && go vet ./... && go test ./...`. Python: `cd modelman && uv run pytest -q` and `make check`.

## Plan-time decisions (resolving the spec's deferred items)

- `--json` schema for `expose|unexpose|sync`: `{"outcomes":[{"id":"…","action":"exposed|unexposed","error":"…"}],"changed":true,"warnings":["…"]}`. `list`: `{"routed":["id",…]}`. `status`: `{"enabled":bool,"url":"…","api_key_set":bool}`. `providers`: `{"providers":{"<id>":{"cloud":bool}}}`.
- Exit codes: `0` when every requested id applied (config written or already correct, even if the restart warned); `1` when the config could not be processed, or any id failed (JSON still printed, listing per-id errors). This refines the spec's "1 means nothing changed": a partial batch applies the valid ids and exits 1.
- `sync` is explicit only (never automatic).
- `config.yaml` is round-tripped with `yaml.v3` (verified against the live config: content identical, sequences re-indented `- ` → `  - `, blank lines not preserved, comments preserved). The first wt write reformats the file whitespace-only; `WriteFile` preserves the file's permission bits (it holds an API key).
- The proxy takes a few seconds to come back after a restart; after a route change the lifecycle hook waits (≤30 s) on `GET <litellm url>/health/liveliness` when a LiteLLM URL is configured.

## File Structure

**Create (Go):**
- `wt/internal/litellm/policy.go` — provider→LiteLLM mapping table.
- `wt/internal/litellm/entry.go` — registry model → `model_list` row node.
- `wt/internal/litellm/configfile.go` — `config.yaml` open/edit/save/lock.
- `wt/internal/litellm/restart.go` — restart command + proxy readiness wait.
- `wt/internal/litellm/service.go` — `Apply`, `Sync`, `Check`, `LocalModels`, `ModelFor`, `Providers`.
- `wt/internal/litellm/*_test.go`, `wt/internal/litellm/testmain_test.go`.
- `wt/cmd/wt/litellm.go`, `wt/cmd/wt/litellm_test.go` — the `wt litellm` subcommands.
- `wt/internal/lifecycle/routes.go`, `routes_test.go` — start/stop route hooks.
- `docs/contracts/litellm-cli.sample.json` — cross-language JSON contract.

**Modify (Go):** `wt/go.mod`, `wt/internal/config/config.go` (Model.ModelInfo, ReadyFlag, SetExposureForTest, LitellmTable), `wt/internal/config/modelman.go`, `wt/internal/lifecycle/lifecycle.go` (public wrappers), `wt/cmd/wt/main.go` (register command), `wt/CLAUDE.md`.

**Create (Python):** `modelman/src/modelman/wt_bridge.py`, `modelman/tests/test_wt_bridge.py`, `modelman/tests/contracts/test_litellm_cli_fixture.py`.

**Modify (Python):** `modelman/src/modelman/litellm.py`, `local_control.py` (comments only), `main.py`, `screens/models.py`, `state.py` (inert passthrough note), `modelman/tests/conftest.py`, `test_litellm.py`, `test_expose.py`, `test_queue.py`, `test_litellm_cli.py`, `modelman/CLAUDE.md`, guides.

---

# PHASE 1 — `internal/litellm` and `wt litellm expose|unexpose|sync|list`

### Task 1: Foundations — deps, config accessors, policy table, row builder

**Files:**
- Modify: `wt/go.mod` (add `gopkg.in/yaml.v3`)
- Modify: `wt/internal/config/config.go` (Model struct ~line 268, Config accessors near `ExposedFlag`)
- Create: `wt/internal/litellm/policy.go`, `entry.go`, `testmain_test.go`, `entry_test.go`

**Interfaces:**
- Produces (config): `Model.ModelInfo map[string]any`; `(*Config).ReadyFlag(id string) bool`; `(*Config).SetExposureForTest(id string, e ExposureEntry)`.
- Produces (litellm): `type Policy struct{Prefix string; FixedModel bool; APIKey string; SecretRef bool; Cloud bool}`; `PolicyFor(providerID string) (Policy, bool)`; `BuildEntry(m config.Model, p config.Provider) (*yaml.Node, error)`.

- [ ] **Step 1: Add the YAML dependency**

```bash
cd wt && go get gopkg.in/yaml.v3@v3.0.1 && go mod tidy
```

- [ ] **Step 2: Add config accessors (test first)**

Append to `wt/internal/config/mutation_test.go` (or a new `wt/internal/config/ready_test.go`):

```go
// TestReadyFlagReadsModelmanState pins that ReadyFlag mirrors modelman's
// per-model `ready` flag (legacy `downloaded` ORed at load). The LiteLLM
// expose gate depends on it: a wrong answer would either route models that
// are not on disk or refuse ones that are.
func TestReadyFlagReadsModelmanState(t *testing.T) {
	c := &Config{}
	c.SetExposureForTest("a/b", ExposureEntry{Ready: true})
	if !c.ReadyFlag("a/b") {
		t.Fatal("ReadyFlag(a/b) = false, want true")
	}
	if c.ReadyFlag("missing") {
		t.Fatal("ReadyFlag(missing) = true, want false")
	}
}
```

Run: `cd wt && go test ./internal/config -run TestReadyFlagReadsModelmanState` → FAIL (undefined). Then add to `config.go` directly below `ExposedFlag`:

```go
// ReadyFlag reports modelman's `ready` flag for the model id (legacy
// `downloaded` ORed in at load). The LiteLLM expose gate uses it: non-cloud
// models must be ready before they may be routed.
func (c *Config) ReadyFlag(id string) bool {
	st, ok := c.exposed[id]
	return ok && st.Ready
}

// SetExposureForTest overrides one model's modelman exposure/ready state.
// Production wiring goes through finalizeCfg (loadModelmanState); tests in
// other packages cannot set the unexported map directly.
func (c *Config) SetExposureForTest(id string, e ExposureEntry) {
	if c.exposed == nil {
		c.exposed = map[string]ExposureEntry{}
	}
	c.exposed[id] = e
}
```

And add the field to `Model` (after `Cost`):

```go
	// ModelInfo is the registry's free-form `[models.model_info]` table.
	// LiteLLM rows merge it over the derived pricing keys, so hand-written
	// keys (context windows, capability flags) reach the proxy.
	ModelInfo map[string]any `toml:"model_info,omitempty"`
```

Run: `cd wt && go test ./internal/config` → PASS. (If a registry-fixture test now fails on the new field, update its expected struct — the field is newly decoded from `[models.model_info]`.)

- [ ] **Step 3: Write the failing row-builder tests**

`wt/internal/litellm/testmain_test.go`:

```go
package litellm

import (
	"context"
	"errors"
	"os"
	"testing"
)

// TestMain replaces the shell runner with a hard failure so no test in this
// package can ever run the real restart command (launchctl kickstart would
// bounce the developer's live LiteLLM proxy and kill in-flight agent
// requests). Tests that exercise restart set runShell themselves.
func TestMain(m *testing.M) {
	runShell = func(context.Context, string) error {
		return errors.New("runShell not stubbed in this test")
	}
	os.Exit(m.Run())
}
```

`wt/internal/litellm/entry_test.go`:

```go
package litellm

import (
	"reflect"
	"testing"

	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"gopkg.in/yaml.v3"
)

func f64(v float64) *float64 { return &v }

// testConfig is the shared registry fixture: two local providers, one cloud,
// one native, with one model each.
func testConfig() *config.Config {
	return &config.Config{
		Providers: []config.Provider{
			{ID: "ollama", Location: config.LocationLocal, Auth: config.AuthConfig{Type: "none", BaseURL: "http://localhost:11434"}},
			{ID: "mtplx", Location: config.LocationLocal, Auth: config.AuthConfig{Type: "none", BaseURL: "http://localhost:8003/v1"}},
			{ID: "openrouter", Location: config.LocationCloud, Auth: config.AuthConfig{Type: "api_key", SecretRef: "sk-test", BaseURL: "https://openrouter.ai/api/v1"}},
			{ID: "claude", Auth: config.AuthConfig{Type: "native"}},
		},
		Models: []config.Model{
			{ID: "ollama/gemma:9b", ProviderID: "ollama", ModelName: "gemma:9b", Location: config.LocationLocal},
			{ID: "mtplx/Youssofal--Q", ProviderID: "mtplx", ModelName: "Youssofal/Q", Location: config.LocationLocal},
			{
				ID: "openrouter/x/y", ProviderID: "openrouter", ModelName: "x/y", Location: config.LocationCloud,
				Cost:      config.ModelCost{InputPricePerMillion: f64(1), OutputPricePerMillion: f64(2), CachePricePerMillion: f64(0.5)},
				ModelInfo: map[string]any{"supports_vision": true, "input_cost_per_token": 0.25},
			},
			{ID: "claude/sonnet", ProviderID: "claude", ModelName: "sonnet", Native: true},
		},
	}
}

func decode(t *testing.T, n *yaml.Node) map[string]any {
	t.Helper()
	var m map[string]any
	if err := n.Decode(&m); err != nil {
		t.Fatal(err)
	}
	return m
}

// TestBuildEntryCloudRow pins the exact row shape for a priced cloud model:
// prefixed model, provider api_base, the secret_ref as api_key, per-token
// pricing converted from per-million, cache price on both cache keys, and
// the model's own model_info overriding derived pricing. LiteLLM cost
// tracking and budget checks depend on these keys being right.
func TestBuildEntryCloudRow(t *testing.T) {
	cfg := testConfig()
	node, err := BuildEntry(cfg.Models[2], cfg.Providers[2])
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]any{
		"model_name": "openrouter/x/y",
		"litellm_params": map[string]any{
			"model":    "openrouter/x/y",
			"api_base": "https://openrouter.ai/api/v1",
			"api_key":  "sk-test",
		},
		"model_info": map[string]any{
			"input_cost_per_token":            0.25, // model_info overrides derived 1e-06
			"output_cost_per_token":           2.0 / 1_000_000,
			"cache_creation_input_token_cost": 0.5 / 1_000_000,
			"cache_read_input_token_cost":     0.5 / 1_000_000,
			"supports_vision":                 true,
		},
	}
	if got := decode(t, node); !reflect.DeepEqual(got, want) {
		t.Fatalf("row =\n%v\nwant\n%v", got, want)
	}
}

// TestBuildEntryLocalRows pins the local mappings: ollama uses the
// ollama_chat/ prefix and no api_key; mtplx uses openai/ with the
// "not-needed" key; unpriced models get explicit zero costs so LiteLLM
// bypasses budget checks for them.
func TestBuildEntryLocalRows(t *testing.T) {
	cfg := testConfig()
	oll, err := BuildEntry(cfg.Models[0], cfg.Providers[0])
	if err != nil {
		t.Fatal(err)
	}
	params := decode(t, oll)["litellm_params"].(map[string]any)
	if params["model"] != "ollama_chat/gemma:9b" || params["api_base"] != "http://localhost:11434" {
		t.Fatalf("ollama params = %v", params)
	}
	if _, has := params["api_key"]; has {
		t.Fatal("ollama row must not carry an api_key")
	}
	info := decode(t, oll)["model_info"].(map[string]any)
	if info["input_cost_per_token"] != 0 || info["output_cost_per_token"] != 0 {
		t.Fatalf("unpriced model_info = %v, want explicit zeros", info)
	}

	mt, err := BuildEntry(cfg.Models[1], cfg.Providers[1])
	if err != nil {
		t.Fatal(err)
	}
	mp := decode(t, mt)["litellm_params"].(map[string]any)
	if mp["model"] != "openai/Youssofal/Q" || mp["api_key"] != "not-needed" {
		t.Fatalf("mtplx params = %v", mp)
	}
}

// TestBuildEntryRejectsUnmappedProvider guards the "no LiteLLM mapping"
// invariant: a provider missing from the policy table must error, never
// produce a half-built row.
func TestBuildEntryRejectsUnmappedProvider(t *testing.T) {
	cfg := testConfig()
	if _, err := BuildEntry(cfg.Models[3], cfg.Providers[3]); err == nil {
		t.Fatal("BuildEntry(native claude) = nil error, want no-mapping error")
	}
}
```

- [ ] **Step 4: Run — expect FAIL** (`undefined: BuildEntry`, `runShell`)

Run: `cd wt && go test ./internal/litellm/ 2>&1 | head` → build failure.

- [ ] **Step 5: Implement**

`wt/internal/litellm/policy.go`:

```go
// Package litellm owns wt's management of LiteLLM's config.yaml: which
// registry models have a model_list route, how each row is built, and
// restarting the proxy after a change. It ports modelman's litellm.py.
package litellm

// Policy describes how one registry provider maps onto a LiteLLM row.
//   - Prefix     — LiteLLM `model` prefix (verbatim model string when FixedModel).
//   - APIKey     — literal api_key to write, "" to omit.
//   - SecretRef  — api_key comes from the provider's auth.secret_ref instead.
//   - Cloud      — the model lives remotely: exempt from the ready gate.
type Policy struct {
	Prefix     string
	FixedModel bool
	APIKey     string
	SecretRef  bool
	Cloud      bool
}

// policies is the single source of truth for provider exposure rules
// (modelman consults it through `wt litellm providers`). Native providers
// are deliberately absent: they never route through LiteLLM.
var policies = map[string]Policy{
	"ollama":        {Prefix: "ollama_chat/"},
	"omlx":          {Prefix: "openai/", APIKey: "not-needed"},
	"mlx_lm_server": {Prefix: "openai/", APIKey: "not-needed"},
	"mtplx":         {Prefix: "openai/", APIKey: "not-needed"},
	"llamacpp":      {Prefix: "openai/local-model", FixedModel: true, APIKey: "dummy-key"},
	"openrouter":    {Prefix: "openrouter/", SecretRef: true, Cloud: true},
}

// PolicyFor returns the mapping for providerID.
func PolicyFor(providerID string) (Policy, bool) {
	p, ok := policies[providerID]
	return p, ok
}
```

`wt/internal/litellm/entry.go`:

```go
package litellm

import (
	"fmt"
	"sort"

	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"gopkg.in/yaml.v3"
)

const tokensPerMillion = 1_000_000

// kv is one ordered mapping entry; val is a plain Go value or a *yaml.Node.
type kv struct {
	key string
	val any
}

// mapping builds an ordered YAML mapping node.
func mapping(pairs []kv) *yaml.Node {
	n := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
	for _, p := range pairs {
		n.Content = append(n.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: p.key}, toNode(p.val))
	}
	return n
}

func toNode(v any) *yaml.Node {
	if n, ok := v.(*yaml.Node); ok {
		return n
	}
	n := &yaml.Node{}
	if err := n.Encode(v); err != nil {
		// Encode only fails on unsupported Go types; registry values are
		// TOML scalars, so surface it loudly rather than writing a bad row.
		panic(fmt.Sprintf("litellm: cannot encode %T: %v", v, err))
	}
	return n
}

// pricingInfo derives model_info pricing keys from a registry cost: per-token
// prices from per-million, cache price on both cache keys, and explicit zeros
// when a price is absent so LiteLLM bypasses budget checks.
func pricingInfo(c config.ModelCost) []kv {
	perToken := func(p *float64) any {
		if p == nil {
			return 0
		}
		return *p / tokensPerMillion
	}
	info := []kv{
		{"input_cost_per_token", perToken(c.InputPricePerMillion)},
		{"output_cost_per_token", perToken(c.OutputPricePerMillion)},
	}
	if c.CachePricePerMillion != nil {
		v := *c.CachePricePerMillion / tokensPerMillion
		info = append(info, kv{"cache_creation_input_token_cost", v}, kv{"cache_read_input_token_cost", v})
	}
	return info
}

// BuildEntry builds the LiteLLM `model_list` row for a registry model:
// model_name is the registry id, litellm_params come from the provider
// policy, and model_info is derived pricing overridden by the model's own
// model_info keys (existing keys keep their position, new ones append in
// sorted order for stable output).
func BuildEntry(m config.Model, p config.Provider) (*yaml.Node, error) {
	pol, ok := PolicyFor(p.ID)
	if !ok {
		return nil, fmt.Errorf("provider %q has no LiteLLM mapping", p.ID)
	}
	model := pol.Prefix
	if !pol.FixedModel {
		model = pol.Prefix + m.ModelName
	}
	params := []kv{{"model", model}}
	if p.Auth.BaseURL != "" {
		params = append(params, kv{"api_base", p.Auth.BaseURL})
	}
	switch {
	case pol.SecretRef:
		params = append(params, kv{"api_key", p.Auth.SecretRef})
	case pol.APIKey != "":
		params = append(params, kv{"api_key", pol.APIKey})
	}

	info := pricingInfo(m.Cost)
	extra := make([]string, 0, len(m.ModelInfo))
	for k := range m.ModelInfo {
		extra = append(extra, k)
	}
	sort.Strings(extra)
	for _, k := range extra {
		replaced := false
		for i := range info {
			if info[i].key == k {
				info[i].val, replaced = m.ModelInfo[k], true
				break
			}
		}
		if !replaced {
			info = append(info, kv{k, m.ModelInfo[k]})
		}
	}

	return mapping([]kv{
		{"model_name", m.ID},
		{"litellm_params", mapping(params)},
		{"model_info", mapping(info)},
	}), nil
}
```

`runShell` is declared in `restart.go` (Task 3). To let this task compile now, create a stub `wt/internal/litellm/restart.go`:

```go
package litellm

import "context"

// runShell runs a shell command; a package var so tests never execute the
// real restart command. Replaced by the real implementation in restart.go's
// full form (Task 3).
var runShell = func(ctx context.Context, cmd string) error { return nil }
```

- [ ] **Step 6: Run — expect PASS**

Run: `cd wt && go test ./internal/litellm/ ./internal/config/ -v 2>&1 | tail -20` → PASS.

- [ ] **Step 7: Commit**

```bash
git add wt/go.mod wt/go.sum wt/internal/config wt/internal/litellm
git commit -m "feat(wt): litellm policy table and row builder — completes plan item #1

Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>"
```

---

### Task 2: `config.yaml` editor (open, set/remove rows, ensure settings, atomic save, lock)

**Files:**
- Create: `wt/internal/litellm/configfile.go`, `configfile_test.go`

**Interfaces:**
- Consumes: `mapping`, `kv`, `toNode` from Task 1.
- Produces:
  - `DefaultPath() string`
  - `var ErrMissing, ErrInvalid error`
  - `Open(path string) (*File, error)`
  - `(*File) RoutedIDs() []string`
  - `(*File) SetRow(id string, row *yaml.Node) error`
  - `(*File) RemoveRow(id string)`
  - `(*File) EnsureSettings()`
  - `(*File) Changed() bool`
  - `(*File) Save() error`
  - `WithLock(path string, fn func() error) error`

- [ ] **Step 1: Write the failing tests**

`wt/internal/litellm/configfile_test.go`:

```go
package litellm

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
)

const baseConfig = `# hand-written header comment
model_list:
  - model_name: ollama/old:1
    litellm_params:
      model: ollama_chat/old:1
      api_base: http://localhost:11434
      additional_drop_params: [custom_param]
  - model_name: keep/me
    litellm_params:
      model: openai/gpt-4o
general_settings:
  master_key: sk-secret # do not lose this
litellm_settings:
  drop_params: true
`

func writeConfig(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func rowFor(t *testing.T, id string) (cfgModel string) {
	t.Helper()
	cfg := testConfig()
	i := config.IndexModelByID(cfg.Models, id)
	n, err := BuildEntry(cfg.Models[i], *cfg.ProviderByID(cfg.Models[i].ProviderID))
	if err != nil {
		t.Fatal(err)
	}
	f, _ := Open(writeConfig(t, "model_list: []\n"))
	if err := f.SetRow(id, n); err != nil {
		t.Fatal(err)
	}
	return string(f.encode())
}

// TestOpenErrors pins the two refusal modes: a missing file and unparseable
// or non-mapping YAML must each return a typed error and never be
// overwritten by a later save — wt must not clobber a config it cannot read.
func TestOpenErrors(t *testing.T) {
	if _, err := Open(filepath.Join(t.TempDir(), "nope.yaml")); !errors.Is(err, ErrMissing) {
		t.Fatalf("missing file err = %v, want ErrMissing", err)
	}
	for _, body := range []string{"a: [unclosed", "- just\n- a list\n", ""} {
		if _, err := Open(writeConfig(t, body)); !errors.Is(err, ErrInvalid) {
			t.Fatalf("Open(%q) err = %v, want ErrInvalid", body, err)
		}
	}
}

// TestSetRowAddsAndReplacesPreservingComments covers the core edit: a new row
// is appended, an existing row is replaced in place, and hand-written comments
// and unrelated sections (general_settings, the master_key comment) survive.
// Losing those would silently break the proxy's auth or the user's notes.
func TestSetRowAddsAndReplacesPreservingComments(t *testing.T) {
	p := writeConfig(t, baseConfig)
	f, err := Open(p)
	if err != nil {
		t.Fatal(err)
	}
	cfg := testConfig()
	row, _ := BuildEntry(cfg.Models[0], cfg.Providers[0]) // ollama/gemma:9b
	if err := f.SetRow("ollama/gemma:9b", row); err != nil {
		t.Fatal(err)
	}
	if err := f.Save(); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(p)
	for _, want := range []string{"# hand-written header comment", "# do not lose this", "master_key: sk-secret", "model_name: keep/me", "model_name: ollama/gemma:9b"} {
		if !strings.Contains(string(got), want) {
			t.Errorf("saved config lost %q:\n%s", want, got)
		}
	}
	f2, _ := Open(p)
	if ids := f2.RoutedIDs(); len(ids) != 3 || ids[2] != "ollama/gemma:9b" {
		t.Fatalf("RoutedIDs = %v, want [ollama/old:1 keep/me ollama/gemma:9b]", ids)
	}
}

// TestSetRowPreservesUserManagedParams pins the presence-based rule: when a
// row is re-exposed, a user-set additional_drop_params list (an extended list
// or a deliberate empty one) and use_chat_completions_api must not be reset to
// defaults, or a deliberate opt-out would silently flip back on every start.
func TestSetRowPreservesUserManagedParams(t *testing.T) {
	p := writeConfig(t, baseConfig)
	f, _ := Open(p)
	cfg := testConfig()
	row, _ := BuildEntry(config.Model{ID: "ollama/old:1", ProviderID: "ollama", ModelName: "old:1"}, cfg.Providers[0])
	if err := f.SetRow("ollama/old:1", row); err != nil {
		t.Fatal(err)
	}
	f.EnsureSettings()
	out := string(f.encode())
	if !strings.Contains(out, "custom_param") {
		t.Fatalf("user additional_drop_params lost:\n%s", out)
	}
	if strings.Contains(out, "reasoning_effort") {
		t.Fatalf("EnsureSettings overwrote a user-managed additional_drop_params:\n%s", out)
	}
}

// TestRemoveRowAndNoOps pins removal (only the named row goes) and the no-op
// contract: removing an absent id changes nothing, so no write and no proxy
// restart follow.
func TestRemoveRowAndNoOps(t *testing.T) {
	f, _ := Open(writeConfig(t, baseConfig))
	f.RemoveRow("does/not/exist")
	if f.Changed() {
		t.Fatal("removing an absent id changed the document")
	}
	f.RemoveRow("keep/me")
	if !f.Changed() {
		t.Fatal("removing a present id did not change the document")
	}
	if ids := f.RoutedIDs(); len(ids) != 1 || ids[0] != "ollama/old:1" {
		t.Fatalf("RoutedIDs = %v", ids)
	}
}

// TestEnsureSettings pins the launcher-required settings: drop_params and the
// anthropic-messages bridge flag are value-enforced (set to true even when
// false or missing); ollama_chat/ rows gain additional_drop_params and
// loopback openai/ rows gain use_chat_completions_api only when the key is
// absent; a real (non-loopback) openai/ row is left alone. Without these the
// proxy 400s/404s for codex, claude and copilot against local backends.
func TestEnsureSettings(t *testing.T) {
	f, _ := Open(writeConfig(t, `model_list:
  - model_name: a
    litellm_params: {model: ollama_chat/a}
  - model_name: b
    litellm_params: {model: openai/b, api_base: "http://127.0.0.1:8003/v1"}
  - model_name: c
    litellm_params: {model: openai/gpt-4o, api_base: "https://api.openai.com/v1"}
litellm_settings:
  drop_params: false
`))
	f.EnsureSettings()
	out := string(f.encode())
	for _, want := range []string{"drop_params: true", "use_chat_completions_url_for_anthropic_messages: true", "reasoning_effort", "use_chat_completions_api: true"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	if strings.Count(out, "use_chat_completions_api") != 1 {
		t.Errorf("real OpenAI row must not get the bridge flag:\n%s", out)
	}
	f2, _ := Open(writeConfig(t, out))
	f2.EnsureSettings()
	if f2.Changed() {
		t.Fatal("EnsureSettings is not idempotent")
	}
}

// TestModelListDegenerateShapes pins tolerance for hand-edited configs: an
// absent or null model_list is created, a scalar model_list is refused with
// ErrInvalid (never overwritten), and non-mapping rows are preserved.
func TestModelListDegenerateShapes(t *testing.T) {
	cfg := testConfig()
	row, _ := BuildEntry(cfg.Models[0], cfg.Providers[0])
	for _, body := range []string{"general_settings: {}\n", "model_list:\n"} {
		f, _ := Open(writeConfig(t, body))
		if err := f.SetRow("ollama/gemma:9b", row); err != nil {
			t.Fatalf("SetRow on %q: %v", body, err)
		}
	}
	f, _ := Open(writeConfig(t, "model_list: nope\n"))
	if err := f.SetRow("ollama/gemma:9b", row); !errors.Is(err, ErrInvalid) {
		t.Fatalf("scalar model_list err = %v, want ErrInvalid", err)
	}
	f, _ = Open(writeConfig(t, "model_list:\n  - just-a-string\n"))
	f.RemoveRow("x")
	if !strings.Contains(string(f.encode()), "just-a-string") {
		t.Fatal("non-mapping row was dropped")
	}
}

// TestSavePreservesPermissions pins that the saved file keeps its mode: the
// config holds the proxy master key and provider API keys, so a save must
// never widen 0600 to the umask default.
func TestSavePreservesPermissions(t *testing.T) {
	p := writeConfig(t, baseConfig)
	f, _ := Open(p)
	f.RemoveRow("keep/me")
	if err := f.Save(); err != nil {
		t.Fatal(err)
	}
	st, _ := os.Stat(p)
	if st.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %v, want 0600", st.Mode().Perm())
	}
}

// TestWithLockSerializes pins that concurrent read-modify-write cycles do not
// interleave: 20 goroutines each append one row under the lock and all 20
// must be present afterwards. Without the lock, two wt processes racing on
// start/stop would lose rows.
func TestWithLockSerializes(t *testing.T) {
	p := writeConfig(t, "model_list: []\n")
	cfg := testConfig()
	row, _ := BuildEntry(cfg.Models[0], cfg.Providers[0])
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			id := string(rune('a'+i)) + "/m"
			if err := WithLock(p, func() error {
				f, err := Open(p)
				if err != nil {
					return err
				}
				if err := f.SetRow(id, row); err != nil {
					return err
				}
				return f.Save()
			}); err != nil {
				t.Error(err)
			}
		}(i)
	}
	wg.Wait()
	f, _ := Open(p)
	if n := len(f.RoutedIDs()); n != 20 {
		t.Fatalf("rows = %d, want 20 (lost updates)", n)
	}
}

// TestDefaultPathEnv pins the path precedence: WT_LITELLM_CONFIG, then the
// legacy MODELMAN_LITELLM_CONFIG, then ~/.config/litellm/config.yaml.
func TestDefaultPathEnv(t *testing.T) {
	t.Setenv("HOME", "/h")
	t.Setenv("WT_LITELLM_CONFIG", "")
	t.Setenv("MODELMAN_LITELLM_CONFIG", "")
	if got := DefaultPath(); got != "/h/.config/litellm/config.yaml" {
		t.Fatalf("default = %q", got)
	}
	t.Setenv("MODELMAN_LITELLM_CONFIG", "/legacy.yaml")
	if got := DefaultPath(); got != "/legacy.yaml" {
		t.Fatalf("legacy = %q", got)
	}
	t.Setenv("WT_LITELLM_CONFIG", "/wt.yaml")
	if got := DefaultPath(); got != "/wt.yaml" {
		t.Fatalf("wt = %q", got)
	}
}
```

(Delete the unused helper `rowFor` if `go vet` flags it.)

- [ ] **Step 2: Run — expect FAIL** (`undefined: Open`, etc.)

Run: `cd wt && go test ./internal/litellm/ 2>&1 | head`

- [ ] **Step 3: Implement**

`wt/internal/litellm/configfile.go`:

```go
package litellm

import (
	"bytes"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"gopkg.in/yaml.v3"
)

var (
	// ErrMissing: config.yaml does not exist (LiteLLM not set up).
	ErrMissing = errors.New("LiteLLM config not found")
	// ErrInvalid: config.yaml exists but is not a YAML mapping, or its
	// model_list is not a list. wt refuses to write such a file.
	ErrInvalid = errors.New("LiteLLM config is invalid")
)

// DefaultPath resolves config.yaml lazily so env overrides work in tests:
// WT_LITELLM_CONFIG, then legacy MODELMAN_LITELLM_CONFIG, then
// ~/.config/litellm/config.yaml.
func DefaultPath() string {
	for _, k := range []string{"WT_LITELLM_CONFIG", "MODELMAN_LITELLM_CONFIG"} {
		if v := os.Getenv(k); v != "" {
			if exp, err := config.ExpandHome(v); err == nil {
				return exp
			}
			return v
		}
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config", "litellm", "config.yaml")
}

// enforcedSettings are value-enforced under litellm_settings on every write.
var enforcedSettings = []string{
	"drop_params",
	"use_chat_completions_url_for_anthropic_messages",
}

// preservedParamKeys survive a row replacement when the new row lacks them:
// they are presence-based (an existing value of any kind marks the row
// user-managed).
var preservedParamKeys = []string{"additional_drop_params", "use_chat_completions_api"}

var loopbackHosts = map[string]bool{"localhost": true, "127.0.0.1": true, "::1": true}

// File is an open, editable LiteLLM config.yaml.
type File struct {
	path   string
	doc    yaml.Node
	before []byte
}

// Open parses path. It returns ErrMissing or ErrInvalid without touching
// the file.
func Open(path string) (*File, error) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil, fmt.Errorf("%w: %s", ErrMissing, path)
	}
	if err != nil {
		return nil, err
	}
	f := &File{path: path}
	if err := yaml.Unmarshal(data, &f.doc); err != nil {
		return nil, fmt.Errorf("%w: %s: %v", ErrInvalid, path, err)
	}
	if f.doc.Kind != yaml.DocumentNode || len(f.doc.Content) == 0 || f.doc.Content[0].Kind != yaml.MappingNode {
		return nil, fmt.Errorf("%w: %s is not a mapping", ErrInvalid, path)
	}
	f.before = f.encode()
	return f, nil
}

func (f *File) root() *yaml.Node { return f.doc.Content[0] }

// encode serializes the document with a 2-space indent and no line folding.
func (f *File) encode() []byte {
	var b bytes.Buffer
	e := yaml.NewEncoder(&b)
	e.SetIndent(2)
	_ = e.Encode(&f.doc)
	_ = e.Close()
	return b.Bytes()
}

// Changed reports whether the document differs from what Open parsed
// (compared through the same encoder, so pure re-indentation is not a change).
func (f *File) Changed() bool { return !bytes.Equal(f.before, f.encode()) }

func isNull(n *yaml.Node) bool { return n.Kind == yaml.ScalarNode && n.Tag == "!!null" }

func mapGet(m *yaml.Node, key string) *yaml.Node {
	if m == nil || m.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			return m.Content[i+1]
		}
	}
	return nil
}

func mapSet(m *yaml.Node, key string, val *yaml.Node) {
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			m.Content[i+1] = val
			return
		}
	}
	m.Content = append(m.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key}, val)
}

func boolNode(v bool) *yaml.Node { return toNode(v) }

func isTrue(n *yaml.Node) bool {
	return n != nil && n.Kind == yaml.ScalarNode && n.Tag == "!!bool" && n.Value == "true"
}

func rowName(row *yaml.Node) string {
	if n := mapGet(row, "model_name"); n != nil {
		return n.Value
	}
	return ""
}

// modelList returns the model_list sequence, creating it when absent or null.
// A non-list value is ErrInvalid: never edit what we do not understand.
func (f *File) modelList() (*yaml.Node, error) {
	ml := mapGet(f.root(), "model_list")
	switch {
	case ml == nil || isNull(ml):
		seq := &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq"}
		mapSet(f.root(), "model_list", seq)
		return seq, nil
	case ml.Kind != yaml.SequenceNode:
		return nil, fmt.Errorf("%w: model_list is not a list in %s", ErrInvalid, f.path)
	}
	return ml, nil
}

// RoutedIDs returns every model_list row's model_name, in file order.
func (f *File) RoutedIDs() []string {
	ml := mapGet(f.root(), "model_list")
	if ml == nil || ml.Kind != yaml.SequenceNode {
		return nil
	}
	var ids []string
	for _, row := range ml.Content {
		if id := rowName(row); id != "" {
			ids = append(ids, id)
		}
	}
	return ids
}

// SetRow adds the row keyed by id, or replaces the existing one in place,
// carrying over any user-managed presence-based params the new row lacks.
func (f *File) SetRow(id string, row *yaml.Node) error {
	ml, err := f.modelList()
	if err != nil {
		return err
	}
	for i, old := range ml.Content {
		if rowName(old) != id {
			continue
		}
		oldParams, newParams := mapGet(old, "litellm_params"), mapGet(row, "litellm_params")
		if oldParams != nil && newParams != nil {
			for _, k := range preservedParamKeys {
				if v := mapGet(oldParams, k); v != nil && mapGet(newParams, k) == nil {
					mapSet(newParams, k, v)
				}
			}
		}
		ml.Content[i] = row
		return nil
	}
	ml.Content = append(ml.Content, row)
	return nil
}

// RemoveRow deletes the row keyed by id (no-op when absent). Rows that are
// not mappings are never ours and are left in place.
func (f *File) RemoveRow(id string) {
	ml := mapGet(f.root(), "model_list")
	if ml == nil || ml.Kind != yaml.SequenceNode {
		return
	}
	kept := ml.Content[:0]
	for _, row := range ml.Content {
		if row.Kind == yaml.MappingNode && rowName(row) == id {
			continue
		}
		kept = append(kept, row)
	}
	ml.Content = kept
}

func isLoopback(n *yaml.Node) bool {
	if n == nil || n.Kind != yaml.ScalarNode {
		return false
	}
	u, err := url.Parse(n.Value)
	return err == nil && loopbackHosts[u.Hostname()]
}

// EnsureSettings applies the launcher-required LiteLLM settings: two
// value-enforced litellm_settings keys, plus presence-based per-row params
// (additional_drop_params on ollama_chat/ rows, use_chat_completions_api on
// loopback openai/ rows). Tolerates hand-edited degenerate shapes.
func (f *File) EnsureSettings() {
	root := f.root()
	ls := mapGet(root, "litellm_settings")
	if ls == nil || isNull(ls) {
		ls = mapping(nil)
		mapSet(root, "litellm_settings", ls)
	}
	if ls.Kind == yaml.MappingNode {
		for _, k := range enforcedSettings {
			if !isTrue(mapGet(ls, k)) {
				mapSet(ls, k, boolNode(true))
			}
		}
	}
	ml := mapGet(root, "model_list")
	if ml == nil || ml.Kind != yaml.SequenceNode {
		return
	}
	for _, row := range ml.Content {
		params := mapGet(row, "litellm_params")
		if params == nil || params.Kind != yaml.MappingNode {
			continue
		}
		model := mapGet(params, "model")
		if model == nil || model.Kind != yaml.ScalarNode {
			continue
		}
		switch {
		case strings.HasPrefix(model.Value, "ollama_chat/") && mapGet(params, "additional_drop_params") == nil:
			seq := &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq", Content: []*yaml.Node{toNode("reasoning_effort")}}
			mapSet(params, "additional_drop_params", seq)
		case strings.HasPrefix(model.Value, "openai/") && mapGet(params, "use_chat_completions_api") == nil && isLoopback(mapGet(params, "api_base")):
			mapSet(params, "use_chat_completions_api", boolNode(true))
		}
	}
}

// Save writes the document atomically (temp file + rename) keeping the
// original file's permission bits: the file holds API keys.
func (f *File) Save() error {
	mode := os.FileMode(0o600)
	if st, err := os.Stat(f.path); err == nil {
		mode = st.Mode().Perm()
	}
	tmp, err := os.CreateTemp(filepath.Dir(f.path), ".litellm-config-*.yaml")
	if err != nil {
		return err
	}
	name := tmp.Name()
	if _, err := tmp.Write(f.encode()); err != nil {
		tmp.Close()
		os.Remove(name)
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(name)
		return err
	}
	if err := os.Chmod(name, mode); err != nil {
		os.Remove(name)
		return err
	}
	if err := os.Rename(name, f.path); err != nil {
		os.Remove(name)
		return err
	}
	return nil
}

// WithLock serializes read-modify-write cycles on path across processes with
// an flock on path+".lock".
func WithLock(path string, fn func() error) error {
	lf, err := os.OpenFile(path+".lock", os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return err
	}
	defer lf.Close()
	if err := syscall.Flock(int(lf.Fd()), syscall.LOCK_EX); err != nil {
		return err
	}
	defer syscall.Flock(int(lf.Fd()), syscall.LOCK_UN) //nolint:errcheck
	return fn()
}
```

- [ ] **Step 4: Run — expect PASS**

Run: `cd wt && go test ./internal/litellm/ -race -v 2>&1 | tail -30` → PASS. If `TestSetRowPreservesUserManagedParams` shows the replaced row lost `custom_param`, the preserve loop is wrong — fix `SetRow`, do not weaken the test.

- [ ] **Step 5: Commit**

```bash
git add wt/internal/litellm
git commit -m "feat(wt): comment-preserving LiteLLM config.yaml editor — completes plan item #2

Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>"
```

---

### Task 3: Restart, readiness wait, and the service layer (`Apply`, `Sync`, `Check`)

**Files:**
- Modify (replace stub): `wt/internal/litellm/restart.go`
- Create: `wt/internal/litellm/service.go`, `restart_test.go`, `service_test.go`

**Interfaces:**
- Consumes: `Open`, `WithLock`, `File` methods, `BuildEntry`, `PolicyFor` (Tasks 1–2); `config.Config.ReadyFlag`, `ResolveLocation`, `ProviderByID`, `IndexModelByID`.
- Produces:
  - `RestartCommand() string`, `Restart() []string`, `WaitReady(ctx context.Context, baseURL string, timeout time.Duration) error`
  - `type Options struct{Path string; SkipReadyGate bool; Restart func() []string}`
  - `type Outcome struct{ID, Action string; Err error}`, `type Result struct{Outcomes []Outcome; Changed bool; Warnings []string}`
  - `Apply(cfg *config.Config, add, remove []string, o Options) (Result, error)`
  - `Check(cfg *config.Config, ids []string, skipReady bool) []Outcome`
  - `Sync(cfg *config.Config, running []string, o Options) (Result, error)`
  - `LocalModels(cfg *config.Config) []config.Model`
  - `ModelFor(cfg *config.Config, providerID, modelName string) (config.Model, bool)`
  - `Providers() map[string]bool` (id → cloud)

- [ ] **Step 1: Write the failing tests**

`wt/internal/litellm/restart_test.go`:

```go
package litellm

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// TestRestartCommandPrecedence pins env precedence (WT_ over legacy
// MODELMAN_, then the launchctl fallback) so an existing modelman user's
// exported restart command keeps working after the move.
func TestRestartCommandPrecedence(t *testing.T) {
	t.Setenv("WT_LITELLM_RESTART_CMD", "")
	t.Setenv("MODELMAN_LITELLM_RESTART_CMD", "")
	if got := RestartCommand(); !strings.Contains(got, "launchctl kickstart -k") {
		t.Fatalf("fallback = %q", got)
	}
	t.Setenv("MODELMAN_LITELLM_RESTART_CMD", "legacy")
	if RestartCommand() != "legacy" {
		t.Fatal("legacy env ignored")
	}
	t.Setenv("WT_LITELLM_RESTART_CMD", "new")
	if RestartCommand() != "new" {
		t.Fatal("WT_ env must win")
	}
}

// TestRestartNeverFails pins that a failing restart command yields a warning
// (naming the manual fix) instead of an error: the config write is the
// source of truth and a stale proxy must not fail wt start/stop.
func TestRestartNeverFails(t *testing.T) {
	t.Setenv("WT_LITELLM_RESTART_CMD", "mycmd")
	var ran string
	runShell = func(_ context.Context, cmd string) error { ran = cmd; return nil }
	if w := Restart(); len(w) != 0 || ran != "mycmd" {
		t.Fatalf("ok restart: warnings=%v ran=%q", w, ran)
	}
	runShell = func(context.Context, string) error { return context.DeadlineExceeded }
	w := Restart()
	if len(w) != 1 || !strings.Contains(w[0], "restart it manually") {
		t.Fatalf("failed restart warnings = %v", w)
	}
}

// TestWaitReady pins the readiness poll: it retries until the proxy answers
// /health/liveliness with 200, and gives up with an error at the timeout, so
// an agent launched right after a restart does not hit connection-refused.
func TestWaitReady(t *testing.T) {
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/health/liveliness" {
			t.Errorf("path = %s", r.URL.Path)
		}
		if atomic.AddInt32(&hits, 1) < 3 {
			w.WriteHeader(http.StatusServiceUnavailable)
		}
	}))
	defer srv.Close()
	if err := WaitReady(context.Background(), srv.URL, 5*time.Second); err != nil {
		t.Fatal(err)
	}
	if atomic.LoadInt32(&hits) < 3 {
		t.Fatalf("hits = %d, want >=3 (must retry through 503)", hits)
	}
	if err := WaitReady(context.Background(), "http://127.0.0.1:1", 300*time.Millisecond); err == nil {
		t.Fatal("WaitReady on a dead port = nil, want timeout error")
	}
}
```

`wt/internal/litellm/service_test.go`:

```go
package litellm

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
)

// opts returns Options with a counting fake restart so no test can bounce a
// real proxy, and the config path pointed at a temp file.
func opts(t *testing.T, body string) (Options, *int, string) {
	t.Helper()
	p := writeConfig(t, body)
	n := 0
	return Options{Path: p, Restart: func() []string { n++; return nil }}, &n, p
}

// TestApplyExposeWritesRowAndRestartsOnce covers the happy path: exposing two
// models writes both rows in one save and restarts the proxy exactly once.
// The single restart matters — each one kills in-flight agent requests.
func TestApplyExposeWritesRowAndRestartsOnce(t *testing.T) {
	o, restarts, p := opts(t, "model_list: []\n")
	res, err := Apply(testConfig(), []string{"ollama/gemma:9b", "openrouter/x/y"}, nil, o)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Changed || *restarts != 1 {
		t.Fatalf("changed=%v restarts=%d, want true/1", res.Changed, *restarts)
	}
	f, _ := Open(p)
	if ids := f.RoutedIDs(); len(ids) != 2 {
		t.Fatalf("routed = %v", ids)
	}
}

// TestApplyIsIdempotent pins that re-exposing an identical row writes nothing
// and does not restart the proxy.
func TestApplyIsIdempotent(t *testing.T) {
	o, restarts, _ := opts(t, "model_list: []\n")
	if _, err := Apply(testConfig(), []string{"ollama/gemma:9b"}, nil, o); err != nil {
		t.Fatal(err)
	}
	*restarts = 0
	res, err := Apply(testConfig(), []string{"ollama/gemma:9b"}, nil, o)
	if err != nil {
		t.Fatal(err)
	}
	if res.Changed || *restarts != 0 {
		t.Fatalf("idempotent re-expose: changed=%v restarts=%d", res.Changed, *restarts)
	}
}

// TestApplyGateRejections pins the expose gate: unknown model, native
// provider and a not-ready local model are each reported per id without
// blocking the valid ids in the same batch, and SkipReadyGate lifts only the
// ready check.
func TestApplyGateRejections(t *testing.T) {
	cfg := testConfig()
	o, _, _ := opts(t, "model_list: []\n")
	res, err := Apply(cfg, []string{"nope/x", "claude/sonnet", "ollama/gemma:9b", "openrouter/x/y"}, nil, o)
	if err != nil {
		t.Fatal(err)
	}
	errs := map[string]string{}
	for _, oc := range res.Outcomes {
		if oc.Err != nil {
			errs[oc.ID] = oc.Err.Error()
		}
	}
	if !strings.Contains(errs["nope/x"], "not found") || !strings.Contains(errs["claude/sonnet"], "native") || !strings.Contains(errs["ollama/gemma:9b"], "not ready") {
		t.Fatalf("errs = %v", errs)
	}
	if _, bad := errs["openrouter/x/y"]; bad {
		t.Fatal("cloud model must be exempt from the ready gate")
	}

	cfg.SetExposureForTest("ollama/gemma:9b", config.ExposureEntry{Ready: true})
	o2, _, _ := opts(t, "model_list: []\n")
	if r, _ := Apply(cfg, []string{"ollama/gemma:9b"}, nil, o2); r.Outcomes[0].Err != nil {
		t.Fatalf("ready model rejected: %v", r.Outcomes[0].Err)
	}
	o3, _, _ := opts(t, "model_list: []\n")
	o3.SkipReadyGate = true
	if r, _ := Apply(testConfig(), []string{"ollama/gemma:9b"}, nil, o3); r.Outcomes[0].Err != nil {
		t.Fatalf("SkipReadyGate still rejected: %v", r.Outcomes[0].Err)
	}
}

// TestApplyUnexposeRemovesRow covers removal and that removing a stale row
// (present in the file, unknown to the registry) still works — the exact
// shape of the stale Qwen3.8-27B route that motivated this work.
func TestApplyUnexposeRemovesRow(t *testing.T) {
	o, restarts, p := opts(t, baseConfig)
	res, err := Apply(testConfig(), nil, []string{"keep/me"}, o)
	if err != nil || !res.Changed || *restarts != 1 {
		t.Fatalf("err=%v changed=%v restarts=%d", err, res.Changed, *restarts)
	}
	f, _ := Open(p)
	for _, id := range f.RoutedIDs() {
		if id == "keep/me" {
			t.Fatal("row not removed")
		}
	}
}

// TestApplyFileLevelFailures pins that a missing or invalid config.yaml
// fails the whole call with a typed error and changes nothing.
func TestApplyFileLevelFailures(t *testing.T) {
	_, err := Apply(testConfig(), []string{"ollama/gemma:9b"}, nil, Options{Path: t.TempDir() + "/none.yaml", SkipReadyGate: true})
	if !errors.Is(err, ErrMissing) {
		t.Fatalf("err = %v, want ErrMissing", err)
	}
	p := writeConfig(t, "model_list: nope\n")
	_, err = Apply(testConfig(), []string{"ollama/gemma:9b"}, nil, Options{Path: p, SkipReadyGate: true})
	if !errors.Is(err, ErrInvalid) {
		t.Fatalf("err = %v, want ErrInvalid", err)
	}
	if b, _ := os.ReadFile(p); string(b) != "model_list: nope\n" {
		t.Fatal("invalid config was overwritten")
	}
}

// TestSyncReconcilesLocalRoutes pins reconciliation: running local models get
// a route, stopped local models lose theirs, cloud rows and unrelated rows are
// untouched. This is what repairs the stale-route drift.
func TestSyncReconcilesLocalRoutes(t *testing.T) {
	o, _, p := opts(t, `model_list:
  - model_name: ollama/gemma:9b
    litellm_params: {model: ollama_chat/gemma:9b}
  - model_name: openrouter/x/y
    litellm_params: {model: openrouter/x/y}
  - model_name: keep/me
    litellm_params: {model: openai/gpt-4o}
`)
	res, err := Sync(testConfig(), []string{"mtplx/Youssofal--Q"}, o)
	if err != nil || !res.Changed {
		t.Fatalf("err=%v changed=%v", err, res.Changed)
	}
	f, _ := Open(p)
	got := strings.Join(f.RoutedIDs(), ",")
	if got != "openrouter/x/y,keep/me,mtplx/Youssofal--Q" {
		t.Fatalf("routed = %s", got)
	}
}

// TestCheckIsSideEffectFree pins --dry-run: Check reports per-id validity and
// never touches the file.
func TestCheckIsSideEffectFree(t *testing.T) {
	out := Check(testConfig(), []string{"claude/sonnet", "openrouter/x/y"}, false)
	if out[0].Err == nil || out[1].Err != nil {
		t.Fatalf("outcomes = %+v", out)
	}
}

// TestModelForAndLocalModels pins the lookup helpers the lifecycle hook uses:
// resolve a registry model by (provider, provider-side name), and list only
// non-native local models with a LiteLLM mapping.
func TestModelForAndLocalModels(t *testing.T) {
	cfg := testConfig()
	if m, ok := ModelFor(cfg, "mtplx", "Youssofal/Q"); !ok || m.ID != "mtplx/Youssofal--Q" {
		t.Fatalf("ModelFor = %v %v", m, ok)
	}
	if _, ok := ModelFor(cfg, "mtplx", "other"); ok {
		t.Fatal("ModelFor matched an unregistered name")
	}
	var ids []string
	for _, m := range LocalModels(cfg) {
		ids = append(ids, m.ID)
	}
	if strings.Join(ids, ",") != "ollama/gemma:9b,mtplx/Youssofal--Q" {
		t.Fatalf("LocalModels = %v", ids)
	}
	if p := Providers(); !p["openrouter"] || p["ollama"] {
		t.Fatalf("Providers = %v (openrouter must be cloud, ollama local)", p)
	}
}

var _ = context.Background
```

- [ ] **Step 2: Run — expect FAIL**

Run: `cd wt && go test ./internal/litellm/ 2>&1 | head`

- [ ] **Step 3: Implement**

Replace `wt/internal/litellm/restart.go`:

```go
package litellm

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"time"
)

// fallbackRestartCmd bounces the launchd-managed proxy. Used when no restart
// env var is set, because non-interactive launches (agents, worktree
// launchers) do not inherit interactive-shell exports.
const fallbackRestartCmd = "launchctl kickstart -k gui/$(id -u)/local.litellm.proxy"

// runShell runs a shell command; a package var so tests never execute the
// real restart command.
var runShell = func(ctx context.Context, cmd string) error {
	return exec.CommandContext(ctx, "/bin/sh", "-c", cmd).Run()
}

// RestartCommand returns the proxy restart command: WT_LITELLM_RESTART_CMD,
// then legacy MODELMAN_LITELLM_RESTART_CMD, then the launchctl fallback.
func RestartCommand() string {
	for _, k := range []string{"WT_LITELLM_RESTART_CMD", "MODELMAN_LITELLM_RESTART_CMD"} {
		if v := os.Getenv(k); v != "" {
			return v
		}
	}
	return fallbackRestartCmd
}

// Restart bounces the proxy after a config write. It never fails the caller:
// the config write is the source of truth, and a failed restart only leaves
// the proxy stale, reported as a warning naming the manual fix.
func Restart() []string {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := RestartCommand()
	if err := runShell(ctx, cmd); err != nil {
		return []string{fmt.Sprintf("failed to restart LiteLLM proxy (%v); restart it manually: %s", err, cmd)}
	}
	return nil
}

// WaitReady polls <baseURL>/health/liveliness until it answers 200 or the
// timeout elapses, so a caller does not launch an agent into a proxy that is
// still coming back up.
func WaitReady(ctx context.Context, baseURL string, timeout time.Duration) error {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	url := strings.TrimRight(baseURL, "/") + "/health/liveliness"
	client := &http.Client{Timeout: 2 * time.Second}
	for {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			return err
		}
		if resp, err := client.Do(req); err == nil {
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return nil
			}
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("LiteLLM proxy not ready at %s: %w", url, ctx.Err())
		case <-time.After(300 * time.Millisecond):
		}
	}
}
```

`wt/internal/litellm/service.go`:

```go
package litellm

import (
	"fmt"
	"slices"

	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"gopkg.in/yaml.v3"
)

// Options tunes Apply/Sync.
//   - Path          — config.yaml; "" means DefaultPath().
//   - SkipReadyGate — skip the "model must be ready" check (the lifecycle
//     hook passes it: the model is verifiably running; modelman passes it
//     because it applied the gate against its own in-memory state).
//   - Restart       — proxy restart hook; nil means Restart. Tests inject.
type Options struct {
	Path          string
	SkipReadyGate bool
	Restart       func() []string
}

// Outcome is one requested id's result. Action is "exposed" or "unexposed"
// (what was asked for and applied); Err is set when the id was rejected.
type Outcome struct {
	ID     string
	Action string
	Err    error
}

// Result reports a batch: per-id outcomes, whether config.yaml was written,
// and non-fatal warnings (restart failures).
type Result struct {
	Outcomes []Outcome
	Changed  bool
	Warnings []string
}

func (o Options) path() string {
	if o.Path != "" {
		return o.Path
	}
	return DefaultPath()
}

// isCloud reports whether a model is exempt from the ready gate: its provider
// policy says cloud, or the model or its provider is located in the cloud.
func isCloud(m config.Model, p config.Provider, pol Policy) bool {
	return pol.Cloud || m.Location == config.LocationCloud || p.Location == config.LocationCloud
}

// prepare validates id against the registry and builds its row.
func prepare(cfg *config.Config, id string, skipReady bool) (*yaml.Node, error) {
	i := config.IndexModelByID(cfg.Models, id)
	if i < 0 {
		return nil, fmt.Errorf("model %q not found in registry", id)
	}
	m := cfg.Models[i]
	p := cfg.ProviderByID(m.ProviderID)
	if p == nil {
		return nil, fmt.Errorf("model %q references unknown provider %q", id, m.ProviderID)
	}
	if m.Native || p.Auth.Type == "native" {
		return nil, fmt.Errorf("provider %q is native and cannot be exposed through LiteLLM", m.ProviderID)
	}
	pol, ok := PolicyFor(p.ID)
	if !ok {
		return nil, fmt.Errorf("provider %q has no LiteLLM mapping", p.ID)
	}
	if !skipReady && !cfg.ReadyFlag(id) && !isCloud(m, *p, pol) {
		return nil, fmt.Errorf("model %q is not ready", id)
	}
	return BuildEntry(m, *p)
}

// Check validates ids without touching any file (`--dry-run`).
func Check(cfg *config.Config, ids []string, skipReady bool) []Outcome {
	out := make([]Outcome, 0, len(ids))
	for _, id := range ids {
		_, err := prepare(cfg, id, skipReady)
		out = append(out, Outcome{ID: id, Action: "exposed", Err: err})
	}
	return out
}

// Apply adds routes for `add` and removes routes for `remove` in one locked
// read-modify-write and one restart. Per-id validation failures are reported
// in Outcomes and do not block the rest. A missing/invalid config.yaml
// returns (Result{}, err) with nothing changed. The file is written, and the
// proxy restarted, only when the document actually changed.
func Apply(cfg *config.Config, add, remove []string, o Options) (Result, error) {
	path := o.path()
	var res Result
	err := WithLock(path, func() error {
		f, err := Open(path)
		if err != nil {
			return err
		}
		for _, id := range remove {
			f.RemoveRow(id)
			res.Outcomes = append(res.Outcomes, Outcome{ID: id, Action: "unexposed"})
		}
		for _, id := range add {
			row, perr := prepare(cfg, id, o.SkipReadyGate)
			if perr != nil {
				res.Outcomes = append(res.Outcomes, Outcome{ID: id, Err: perr})
				continue
			}
			if err := f.SetRow(id, row); err != nil {
				return err
			}
			res.Outcomes = append(res.Outcomes, Outcome{ID: id, Action: "exposed"})
		}
		f.EnsureSettings()
		if !f.Changed() {
			return nil
		}
		if err := f.Save(); err != nil {
			return err
		}
		res.Changed = true
		return nil
	})
	if err != nil {
		return Result{}, err
	}
	if res.Changed {
		restart := o.Restart
		if restart == nil {
			restart = Restart
		}
		res.Warnings = restart()
	}
	return res, nil
}

// LocalModels lists the registry's non-native local models that have a
// LiteLLM mapping — the set Sync manages. Cloud and native models are never
// touched by Sync.
func LocalModels(cfg *config.Config) []config.Model {
	var out []config.Model
	for _, m := range cfg.Models {
		if m.Native {
			continue
		}
		if loc, err := cfg.ResolveLocation(m); err != nil || loc != config.LocationLocal {
			continue
		}
		if _, ok := PolicyFor(m.ProviderID); !ok {
			continue
		}
		out = append(out, m)
	}
	return out
}

// ModelFor finds the registry model for a provider and provider-side name.
func ModelFor(cfg *config.Config, providerID, modelName string) (config.Model, bool) {
	for _, m := range cfg.Models {
		if m.ProviderID == providerID && m.ModelName == modelName {
			return m, true
		}
	}
	return config.Model{}, false
}

// Sync makes the local-model routes match reality: every id in `running`
// (that is a registry local model) gets a route; every other local model that
// currently has a route loses it. Cloud and unrelated rows are untouched.
func Sync(cfg *config.Config, running []string, o Options) (Result, error) {
	f, err := Open(o.path())
	if err != nil {
		return Result{}, err
	}
	routed := f.RoutedIDs()
	var add, remove []string
	for _, m := range LocalModels(cfg) {
		switch {
		case slices.Contains(running, m.ID):
			add = append(add, m.ID)
		case slices.Contains(routed, m.ID):
			remove = append(remove, m.ID)
		}
	}
	o.SkipReadyGate = true
	return Apply(cfg, add, remove, o)
}

// Providers maps each LiteLLM-mapped provider id to whether it is a cloud
// provider. modelman reads this through `wt litellm providers` instead of
// keeping its own copy of the policy table.
func Providers() map[string]bool {
	out := make(map[string]bool, len(policies))
	for id, p := range policies {
		out[id] = p.Cloud
	}
	return out
}
```

Remove the trailing `var _ = context.Background` from `service_test.go` if `context` is otherwise unused (drop the import too).

- [ ] **Step 4: Run — expect PASS**

Run: `cd wt && go test ./internal/litellm/ -race -v 2>&1 | tail -30 && go vet ./internal/litellm/`

- [ ] **Step 5: Commit**

```bash
git add wt/internal/litellm
git commit -m "feat(wt): litellm Apply/Sync/Check service, restart and readiness wait — completes plan item #3

Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>"
```

---

### Task 4: `wt litellm expose|unexpose|sync|list|providers` CLI and the JSON contract

**Files:**
- Create: `wt/cmd/wt/litellm.go`, `wt/cmd/wt/litellm_test.go`, `docs/contracts/litellm-cli.sample.json`
- Modify: `wt/cmd/wt/main.go:394` (register)

**Interfaces:**
- Consumes: `litellm.Apply/Check/Sync/Open/DefaultPath/Providers`; `probeInventory` seam (`cmd/wt/resolve.go:16`); `localmodels.Snapshot`.
- Produces: `litellmCmd(a *app) *cobra.Command`; `runLitellmChange(out, errOut io.Writer, cfg *config.Config, expose bool, ids []string, fl litellmFlags) error`; `runLitellmSync(...)`, `runLitellmList(...)`, `runLitellmProviders(...)`; JSON types `litellmResultJSON`.

- [ ] **Step 1: Write the contract fixture**

`docs/contracts/litellm-cli.sample.json`:

```json
{
  "_comment": "Shared fixture for cross-language contract tests of `wt litellm ... --json`. Read by wt/cmd/wt/litellm_test.go (Go) and modelman/tests/contracts/test_litellm_cli_fixture.py (Python). A schema change on either side fails both CI jobs in the same PR.",
  "change": {
    "outcomes": [
      {"id": "ollama/gemma:9b", "action": "exposed"},
      {"id": "claude/sonnet", "error": "provider \"claude\" is native and cannot be exposed through LiteLLM"}
    ],
    "changed": true,
    "warnings": ["failed to restart LiteLLM proxy (exit status 1); restart it manually: launchctl kickstart -k gui/$(id -u)/local.litellm.proxy"]
  },
  "list": {"routed": ["ollama/gemma:9b", "openrouter/x/y"]},
  "providers": {"providers": {"ollama": {"cloud": false}, "openrouter": {"cloud": true}}},
  "status": {"enabled": true, "url": "http://localhost:4000", "api_key_set": true}
}
```

- [ ] **Step 2: Write the failing tests**

`wt/cmd/wt/litellm_test.go`:

```go
package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/localmodels"
)

func litellmTestConfig() *config.Config {
	return &config.Config{
		Providers: []config.Provider{
			{ID: "ollama", Location: config.LocationLocal, Auth: config.AuthConfig{Type: "none", BaseURL: "http://localhost:11434"}},
			{ID: "claude", Auth: config.AuthConfig{Type: "native"}},
		},
		Models: []config.Model{
			{ID: "ollama/gemma:9b", ProviderID: "ollama", ModelName: "gemma:9b", Location: config.LocationLocal},
			{ID: "claude/sonnet", ProviderID: "claude", ModelName: "sonnet", Native: true},
		},
	}
}

// litellmEnv points the config path at a temp file and the restart command
// at a no-op so no test can touch the real proxy or config.
func litellmEnv(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("WT_LITELLM_CONFIG", p)
	t.Setenv("WT_LITELLM_RESTART_CMD", "true")
	return p
}

// TestLitellmExposeJSONMatchesContract pins the --json shape against the
// shared cross-language fixture: modelman's bridge parses exactly these keys,
// so renaming one here without the fixture failing would break modelman
// silently at runtime.
func TestLitellmExposeJSONMatchesContract(t *testing.T) {
	litellmEnv(t, "model_list: []\n")
	var out, errOut bytes.Buffer
	err := runLitellmChange(&out, &errOut, litellmTestConfig(), true, []string{"ollama/gemma:9b", "claude/sonnet"},
		litellmFlags{JSON: true, SkipReadyGate: true})
	if err == nil {
		t.Fatal("want a non-nil error: one id was rejected")
	}
	var got map[string]any
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatalf("output is not JSON: %v\n%s", err, out.String())
	}
	raw, _ := os.ReadFile("../../../docs/contracts/litellm-cli.sample.json")
	var fixture map[string]map[string]any
	if err := json.Unmarshal(raw, &fixture); err != nil {
		t.Fatal(err)
	}
	for k := range fixture["change"] {
		if _, ok := got[k]; !ok {
			t.Errorf("output lacks contract key %q: %v", k, got)
		}
	}
	outcomes := got["outcomes"].([]any)
	first := outcomes[0].(map[string]any)
	second := outcomes[1].(map[string]any)
	if first["action"] != "exposed" || second["error"] == nil || got["changed"] != true {
		t.Fatalf("got = %v", got)
	}
}

// TestLitellmExposeGateAndDryRun pins that the CLI enforces the ready gate by
// default (a not-ready local model is refused), that --skip-ready-gate lifts
// it, and that --dry-run validates without writing anything.
func TestLitellmExposeGateAndDryRun(t *testing.T) {
	p := litellmEnv(t, "model_list: []\n")
	var out, errOut bytes.Buffer
	if err := runLitellmChange(&out, &errOut, litellmTestConfig(), true, []string{"ollama/gemma:9b"}, litellmFlags{}); err == nil ||
		!strings.Contains(out.String()+errOut.String(), "not ready") {
		t.Fatalf("not-ready model accepted: err=%v out=%s", err, out.String())
	}
	out.Reset()
	if err := runLitellmChange(&out, &errOut, litellmTestConfig(), true, []string{"ollama/gemma:9b"}, litellmFlags{DryRun: true, SkipReadyGate: true}); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(p); string(b) != "model_list: []\n" {
		t.Fatal("--dry-run wrote to config.yaml")
	}
}

// TestLitellmSyncUsesLiveInventory pins that sync derives "running" from the
// live inventory (never modelman's flag): a running registered model gets a
// route and an unrouted-but-stopped one keeps none.
func TestLitellmSyncUsesLiveInventory(t *testing.T) {
	p := litellmEnv(t, "model_list: []\n")
	stubProbeInventory(t, localmodels.Snapshot{Entries: []localmodels.Entry{
		{ProviderID: "ollama", ModelID: "ollama/gemma:9b", ModelName: "gemma:9b", Registered: true, Running: true},
	}})
	var out, errOut bytes.Buffer
	if err := runLitellmSync(&out, &errOut, litellmTestConfig(), false); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(p); !strings.Contains(string(b), "model_name: ollama/gemma:9b") {
		t.Fatalf("running model not routed:\n%s", b)
	}
}

// TestLitellmListAndProviders pins the read-only commands' JSON shapes used by
// modelman's EXPOSED column and its provider policy lookup.
func TestLitellmListAndProviders(t *testing.T) {
	litellmEnv(t, "model_list:\n  - model_name: a/b\n    litellm_params: {model: x}\n")
	var out bytes.Buffer
	if err := runLitellmList(&out, true); err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(out.String()) != `{"routed":["a/b"]}` {
		t.Fatalf("list = %s", out.String())
	}
	out.Reset()
	if err := runLitellmProviders(&out, true); err != nil {
		t.Fatal(err)
	}
	var got struct {
		Providers map[string]struct{ Cloud bool } `json:"providers"`
	}
	if err := json.Unmarshal(out.Bytes(), &got); err != nil || !got.Providers["openrouter"].Cloud || got.Providers["ollama"].Cloud {
		t.Fatalf("providers = %s (%v)", out.String(), err)
	}
}
```

- [ ] **Step 3: Run — expect FAIL**

Run: `cd wt && go test ./cmd/wt -run Litellm 2>&1 | head`

- [ ] **Step 4: Implement**

`wt/cmd/wt/litellm.go`:

```go
// wt litellm — the primitives that manage LiteLLM's config.yaml and the proxy.
// wt owns this since the 2026-09-21 ownership move; modelman shells out to
// these commands (see docs/superpowers/specs/2026-09-21-wt-litellm-ownership-design.md).
package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"

	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/litellm"
	"github.com/spf13/cobra"
)

// litellmFlags carries the change subcommands' flags.
type litellmFlags struct {
	JSON          bool
	DryRun        bool
	SkipReadyGate bool
}

type litellmOutcomeJSON struct {
	ID     string `json:"id"`
	Action string `json:"action,omitempty"`
	Error  string `json:"error,omitempty"`
}

type litellmResultJSON struct {
	Outcomes []litellmOutcomeJSON `json:"outcomes"`
	Changed  bool                 `json:"changed"`
	Warnings []string             `json:"warnings"`
}

var errLitellmIDFailed = errors.New("one or more models could not be applied")

// reportLitellm prints a Result (text or JSON) and returns errLitellmIDFailed
// when any id was rejected, so the process exits 1 while the per-id detail is
// still printed for callers that parse it.
func reportLitellm(out, errOut io.Writer, res litellm.Result, asJSON bool) error {
	failed := false
	doc := litellmResultJSON{Outcomes: []litellmOutcomeJSON{}, Changed: res.Changed, Warnings: append([]string{}, res.Warnings...)}
	for _, o := range res.Outcomes {
		j := litellmOutcomeJSON{ID: o.ID, Action: o.Action}
		if o.Err != nil {
			j.Action, j.Error, failed = "", o.Err.Error(), true
		}
		doc.Outcomes = append(doc.Outcomes, j)
	}
	if asJSON {
		if err := json.NewEncoder(out).Encode(doc); err != nil {
			return err
		}
	} else {
		for _, j := range doc.Outcomes {
			if j.Error != "" {
				fmt.Fprintf(errOut, "%s: %s\n", j.ID, j.Error)
			} else {
				fmt.Fprintf(out, "%s: %s\n", j.ID, j.Action)
			}
		}
		for _, w := range doc.Warnings {
			fmt.Fprintf(errOut, "warning: %s\n", w)
		}
	}
	if failed {
		return errLitellmIDFailed
	}
	return nil
}

func runLitellmChange(out, errOut io.Writer, cfg *config.Config, expose bool, ids []string, fl litellmFlags) error {
	o := litellm.Options{SkipReadyGate: fl.SkipReadyGate}
	var res litellm.Result
	var err error
	switch {
	case expose && fl.DryRun:
		res.Outcomes = litellm.Check(cfg, ids, fl.SkipReadyGate)
	case expose:
		res, err = litellm.Apply(cfg, ids, nil, o)
	default:
		res, err = litellm.Apply(cfg, nil, ids, o)
	}
	if err != nil {
		return err
	}
	return reportLitellm(out, errOut, res, fl.JSON)
}

func runLitellmSync(out, errOut io.Writer, cfg *config.Config, asJSON bool) error {
	var running []string
	for _, e := range probeInventory(cfg).Entries {
		if e.Running && e.Registered {
			running = append(running, e.ModelID)
		}
	}
	res, err := litellm.Sync(cfg, running, litellm.Options{})
	if err != nil {
		return err
	}
	return reportLitellm(out, errOut, res, asJSON)
}

func runLitellmList(out io.Writer, asJSON bool) error {
	f, err := litellm.Open(litellm.DefaultPath())
	if err != nil {
		return err
	}
	ids := f.RoutedIDs()
	if ids == nil {
		ids = []string{}
	}
	if asJSON {
		return json.NewEncoder(out).Encode(map[string][]string{"routed": ids})
	}
	for _, id := range ids {
		fmt.Fprintln(out, id)
	}
	return nil
}

func runLitellmProviders(out io.Writer, asJSON bool) error {
	provs := litellm.Providers()
	if asJSON {
		doc := map[string]map[string]map[string]bool{"providers": {}}
		for id, cloud := range provs {
			doc["providers"][id] = map[string]bool{"cloud": cloud}
		}
		return json.NewEncoder(out).Encode(doc)
	}
	ids := make([]string, 0, len(provs))
	for id := range provs {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		fmt.Fprintf(out, "%s cloud=%v\n", id, provs[id])
	}
	return nil
}

func litellmCmd(a *app) *cobra.Command {
	c := &cobra.Command{
		Use:   "litellm",
		Short: "Manage LiteLLM routes and routing state for registry models",
	}
	change := func(use, short string, expose bool) *cobra.Command {
		var fl litellmFlags
		cc := &cobra.Command{
			Use: use, Short: short, Args: cobra.MinimumNArgs(1), SilenceUsage: true,
			RunE: func(cmd *cobra.Command, args []string) error {
				return runLitellmChange(cmd.OutOrStdout(), cmd.ErrOrStderr(), a.cfg, expose, args, fl)
			},
		}
		cc.Flags().BoolVar(&fl.JSON, "json", false, "machine-readable output")
		if expose {
			cc.Flags().BoolVar(&fl.DryRun, "dry-run", false, "validate only; change nothing")
			cc.Flags().BoolVar(&fl.SkipReadyGate, "skip-ready-gate", false, "caller already verified readiness (used by modelman)")
		}
		return cc
	}
	var syncJSON, listJSON, provJSON bool
	syncC := &cobra.Command{
		Use: "sync", Short: "Make local-model routes match the running models", Args: cobra.NoArgs, SilenceUsage: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runLitellmSync(cmd.OutOrStdout(), cmd.ErrOrStderr(), a.cfg, syncJSON)
		},
	}
	syncC.Flags().BoolVar(&syncJSON, "json", false, "machine-readable output")
	listC := &cobra.Command{
		Use: "list", Short: "List routed model ids from config.yaml", Args: cobra.NoArgs, SilenceUsage: true,
		RunE: func(cmd *cobra.Command, _ []string) error { return runLitellmList(cmd.OutOrStdout(), listJSON) },
	}
	listC.Flags().BoolVar(&listJSON, "json", false, "machine-readable output")
	provC := &cobra.Command{
		Use: "providers", Short: "List providers with a LiteLLM mapping", Args: cobra.NoArgs, SilenceUsage: true,
		RunE: func(cmd *cobra.Command, _ []string) error { return runLitellmProviders(cmd.OutOrStdout(), provJSON) },
	}
	provC.Flags().BoolVar(&provJSON, "json", false, "machine-readable output")
	c.AddCommand(
		change("expose <model-id>...", "Add LiteLLM routes and restart the proxy once", true),
		change("unexpose <model-id>...", "Remove LiteLLM routes and restart the proxy once", false),
		syncC, listC, provC,
	)
	return c
}
```

Register in `wt/cmd/wt/main.go:394`: change to
`cmd.AddCommand(rotateCmd(a), configCmd(a), statsCmd(a), smokeCmd(a), stopCmd(a), startCmd(a), litellmCmd(a))`.

- [ ] **Step 5: Run — expect PASS**, plus the whole wt gate

Run: `cd wt && go build ./... && go vet ./... && go test ./... 2>&1 | tail -15`

- [ ] **Step 6: Smoke the real binary against a scratch config (never the real one)**

```bash
cd wt && go build -o /tmp/wt-litellm-smoke ./cmd/wt
cp ~/.config/litellm/config.yaml /tmp/wt-litellm-scratch.yaml
WT_LITELLM_CONFIG=/tmp/wt-litellm-scratch.yaml /tmp/wt-litellm-smoke litellm list --json | head -c 300
WT_LITELLM_CONFIG=/tmp/wt-litellm-scratch.yaml /tmp/wt-litellm-smoke litellm providers --json
WT_LITELLM_CONFIG=/tmp/wt-litellm-scratch.yaml WT_LITELLM_RESTART_CMD=true \
  /tmp/wt-litellm-smoke litellm expose --skip-ready-gate 'mtplx/Youssofal--Qwen3.6-35B-A3B-MTPLX-Optimized-Balance'
```
Expected: `list` prints routed ids; `providers` prints the mapping; `expose` prints `…: exposed`. Then `grep -c Qwen3.6 /tmp/wt-litellm-scratch.yaml` ≥ 1. Delete the scratch files afterwards (they contain an API key): `rm -f /tmp/wt-litellm-scratch.yaml /tmp/wt-litellm-smoke`.

- [ ] **Step 7: Commit — end of Phase 1**

```bash
git add wt/cmd/wt docs/contracts/litellm-cli.sample.json
git commit -m "feat(wt): wt litellm expose|unexpose|sync|list|providers — completes plan item #4

Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>"
```

**Phase 1 checkpoint:** stop and ask the user before pushing or opening a PR.

---

# PHASE 2 — wire wt start/stop (fixes the reported bug)

### Task 5: Lifecycle route hooks

**Files:**
- Create: `wt/internal/lifecycle/routes.go`, `wt/internal/lifecycle/routes_test.go`
- Modify: `wt/internal/lifecycle/lifecycle.go:209-211` (`Start`), `:247-250` (`Stop`), `:265-267` (`StopModel`)

**Interfaces:**
- Consumes: `litellm.Apply`, `litellm.ModelFor`, `litellm.LocalModels`, `litellm.WaitReady`, `litellm.ErrMissing`, `SingleModel`, `localmodels.Family`.
- Produces: package vars `applyRoutes` (seam over `litellm.Apply`), `waitProxy` (seam over `litellm.WaitReady`), `routesWarn io.Writer`; funcs `routeAfterStart(cfg *config.Config, t Target)`, `routeAfterStop(cfg *config.Config, providerID, modelName string)`.

- [ ] **Step 1: Write the failing tests**

`wt/internal/lifecycle/routes_test.go`:

```go
package lifecycle

import (
	"bytes"
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/litellm"
)

func routesCfg() *config.Config {
	return &config.Config{
		Providers: []config.Provider{
			{ID: "ollama", Location: config.LocationLocal, Auth: config.AuthConfig{Type: "none", BaseURL: "http://localhost:11434"}},
			{ID: "mtplx", Location: config.LocationLocal, Auth: config.AuthConfig{Type: "none", BaseURL: "http://localhost:8003/v1"}},
		},
		Models: []config.Model{
			{ID: "ollama/a:1", ProviderID: "ollama", ModelName: "a:1", Location: config.LocationLocal},
			{ID: "ollama/b:1", ProviderID: "ollama", ModelName: "b:1", Location: config.LocationLocal},
			{ID: "mtplx/Y--Q35", ProviderID: "mtplx", ModelName: "Y/Q35", Location: config.LocationLocal},
			{ID: "mtplx/Y--Q27", ProviderID: "mtplx", ModelName: "Y/Q27", Location: config.LocationLocal},
		},
	}
}

type routeCall struct{ add, remove []string }

// stubRoutes replaces the litellm seams for one test and returns the recorded
// calls and the warning buffer.
func stubRoutes(t *testing.T, res litellm.Result, err error) (*[]routeCall, *bytes.Buffer) {
	t.Helper()
	var calls []routeCall
	var warn bytes.Buffer
	oa, ow, ww := applyRoutes, waitProxy, routesWarn
	applyRoutes = func(_ *config.Config, add, remove []string, o litellm.Options) (litellm.Result, error) {
		if !o.SkipReadyGate {
			t.Error("lifecycle hook must skip the ready gate: the model is verifiably running")
		}
		calls = append(calls, routeCall{add, remove})
		return res, err
	}
	waitProxy = func(context.Context, string, time.Duration) error { return nil }
	routesWarn = &warn
	t.Cleanup(func() { applyRoutes, waitProxy, routesWarn = oa, ow, ww })
	return &calls, &warn
}

// TestRouteAfterStartSingleModelReplacesSiblings pins the bug fix: starting a
// single-model provider's model (mtplx) adds ITS route and removes routes of
// the provider's other models, since starting it stopped whatever ran before.
// Without the removal a replaced model keeps a dead route.
func TestRouteAfterStartSingleModelReplacesSiblings(t *testing.T) {
	calls, _ := stubRoutes(t, litellm.Result{}, nil)
	routeAfterStart(routesCfg(), Target{ProviderID: "mtplx", ModelName: "Y/Q35"})
	want := routeCall{add: []string{"mtplx/Y--Q35"}, remove: []string{"mtplx/Y--Q27"}}
	if len(*calls) != 1 || !slices.Equal((*calls)[0].add, want.add) || !slices.Equal((*calls)[0].remove, want.remove) {
		t.Fatalf("calls = %+v, want %+v", *calls, want)
	}
}

// TestRouteAfterStartMultiTenantOnlyAdds pins that ollama (many models at
// once) only adds the started model's route and never removes siblings that
// may still be loaded.
func TestRouteAfterStartMultiTenantOnlyAdds(t *testing.T) {
	calls, _ := stubRoutes(t, litellm.Result{}, nil)
	routeAfterStart(routesCfg(), Target{ProviderID: "ollama", ModelName: "a:1"})
	if len(*calls) != 1 || !slices.Equal((*calls)[0].add, []string{"ollama/a:1"}) || len((*calls)[0].remove) != 0 {
		t.Fatalf("calls = %+v", *calls)
	}
}

// TestRouteAfterStop pins stop symmetry: stopping an ollama model removes only
// its route; stopping a single-model provider's model removes routes for every
// model of that provider (the whole provider went down).
func TestRouteAfterStop(t *testing.T) {
	calls, _ := stubRoutes(t, litellm.Result{}, nil)
	routeAfterStop(routesCfg(), "ollama", "a:1")
	routeAfterStop(routesCfg(), "mtplx", "Y/Q35")
	if len(*calls) != 2 {
		t.Fatalf("calls = %+v", *calls)
	}
	if !slices.Equal((*calls)[0].remove, []string{"ollama/a:1"}) {
		t.Errorf("ollama stop removed %v", (*calls)[0].remove)
	}
	if got := slices.Clone((*calls)[1].remove); !slices.Equal(got, []string{"mtplx/Y--Q35", "mtplx/Y--Q27"}) {
		t.Errorf("mtplx stop removed %v", got)
	}
}

// TestRouteNeverFailsTheCaller pins the failure contract: a LiteLLM error is a
// stderr warning, a missing config.yaml is silent (LiteLLM not set up), and
// unregistered models warn once. Failing a successful start over a missing
// route would strand a running model.
func TestRouteNeverFailsTheCaller(t *testing.T) {
	_, warn := stubRoutes(t, litellm.Result{}, errors.New("boom"))
	routeAfterStart(routesCfg(), Target{ProviderID: "ollama", ModelName: "a:1"})
	if !bytes.Contains(warn.Bytes(), []byte("boom")) {
		t.Fatalf("no warning for a failed apply: %q", warn.String())
	}

	_, warn = stubRoutes(t, litellm.Result{}, litellm.ErrMissing)
	routeAfterStart(routesCfg(), Target{ProviderID: "ollama", ModelName: "a:1"})
	if warn.Len() != 0 {
		t.Fatalf("missing config.yaml must be silent, got %q", warn.String())
	}

	calls, warn := stubRoutes(t, litellm.Result{}, nil)
	routeAfterStart(routesCfg(), Target{ProviderID: "ollama", ModelName: "unregistered:9"})
	if len(*calls) != 0 || !bytes.Contains(warn.Bytes(), []byte("not in the registry")) {
		t.Fatalf("unregistered: calls=%v warn=%q", *calls, warn.String())
	}
}

// TestRouteWaitsForProxyOnlyWhenChanged pins the readiness wait: it runs after
// a route change when a LiteLLM URL is configured, and is skipped when nothing
// changed (no restart happened) or no URL is set.
func TestRouteWaitsForProxyOnlyWhenChanged(t *testing.T) {
	cfg := routesCfg()
	cfg.SetLitellmForTest(config.LitellmState{URL: "http://localhost:4000"})
	stubRoutes(t, litellm.Result{Changed: true}, nil)
	waited := 0
	waitProxy = func(context.Context, string, time.Duration) error { waited++; return nil }
	routeAfterStart(cfg, Target{ProviderID: "ollama", ModelName: "a:1"})
	if waited != 1 {
		t.Fatalf("waited = %d, want 1", waited)
	}
	stubRoutes(t, litellm.Result{Changed: false}, nil)
	waitProxy = func(context.Context, string, time.Duration) error { waited++; return nil }
	routeAfterStart(cfg, Target{ProviderID: "ollama", ModelName: "a:1"})
	if waited != 1 {
		t.Fatal("waited despite no change")
	}
}
```

- [ ] **Step 2: Run — expect FAIL**

Run: `cd wt && go test ./internal/lifecycle -run 'Route' 2>&1 | head`

- [ ] **Step 3: Implement**

`wt/internal/lifecycle/routes.go`:

```go
package lifecycle

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
	"github.com/ohanaverse/local-ai-setup/wt/internal/litellm"
	"github.com/ohanaverse/local-ai-setup/wt/internal/localmodels"
)

// Seams: production talks to the real config.yaml, proxy and stderr.
var (
	applyRoutes             = litellm.Apply
	waitProxy               = litellm.WaitReady
	routesWarn    io.Writer = os.Stderr
)

// proxyReadyTimeout bounds the wait for the proxy after a route change.
const proxyReadyTimeout = 30 * time.Second

// routeAfterStart adds the started model's LiteLLM route. A single-model
// provider (omlx, mtplx) can serve only one model, so starting one replaced
// whatever ran before: its siblings' routes are removed in the same write.
// It never fails the caller — a missing route is a warning, not a failed start.
func routeAfterStart(cfg *config.Config, t Target) {
	m, ok := litellm.ModelFor(cfg, t.ProviderID, t.ModelName)
	if !ok {
		fmt.Fprintf(routesWarn, "wt: %s/%s is not in the registry; no LiteLLM route added (register it with modelman)\n", t.ProviderID, t.ModelName)
		return
	}
	var remove []string
	if SingleModel(t.ProviderID) {
		for _, id := range familyModelIDs(cfg, t.ProviderID) {
			if id != m.ID {
				remove = append(remove, id)
			}
		}
	}
	applyAndReport(cfg, []string{m.ID}, remove)
}

// routeAfterStop removes the stopped model's route. Stopping a single-model
// provider's model takes the whole provider down, so every one of its models
// loses its route. modelName may be "" for a provider-wide stop.
func routeAfterStop(cfg *config.Config, providerID, modelName string) {
	var remove []string
	switch {
	case SingleModel(providerID):
		remove = familyModelIDs(cfg, providerID)
	default:
		m, ok := litellm.ModelFor(cfg, providerID, modelName)
		if !ok {
			return
		}
		remove = []string{m.ID}
	}
	applyAndReport(cfg, nil, remove)
}

// familyModelIDs lists the LiteLLM-managed local model ids whose provider
// shares providerID's family (omlx and omlx-6bit are one physical server).
func familyModelIDs(cfg *config.Config, providerID string) []string {
	fam := localmodels.Family(providerID)
	var ids []string
	for _, m := range litellm.LocalModels(cfg) {
		if localmodels.Family(m.ProviderID) == fam {
			ids = append(ids, m.ID)
		}
	}
	return ids
}

func applyAndReport(cfg *config.Config, add, remove []string) {
	res, err := applyRoutes(cfg, add, remove, litellm.Options{SkipReadyGate: true})
	if err != nil {
		if !errors.Is(err, litellm.ErrMissing) { // no config.yaml = LiteLLM not set up
			fmt.Fprintf(routesWarn, "wt: LiteLLM route not updated: %v\n", err)
		}
		return
	}
	for _, o := range res.Outcomes {
		if o.Err != nil {
			fmt.Fprintf(routesWarn, "wt: LiteLLM route for %s not updated: %v\n", o.ID, o.Err)
		}
	}
	for _, w := range res.Warnings {
		fmt.Fprintf(routesWarn, "wt: %s\n", w)
	}
	if res.Changed && cfg.LitellmBaseURL() != "" {
		if err := waitProxy(context.Background(), cfg.LitellmBaseURL(), proxyReadyTimeout); err != nil {
			fmt.Fprintf(routesWarn, "wt: %v\n", err)
		}
	}
}
```

Edit `wt/internal/lifecycle/lifecycle.go` public wrappers:

```go
func Start(ctx context.Context, cfg *config.Config, t Target, opts Options) error {
	if err := start(ctx, defaultEnv(), cfg, t, opts); err != nil {
		return err
	}
	routeAfterStart(cfg, t)
	return nil
}
```
```go
func Stop(ctx context.Context, cfg *config.Config, providerID string) error {
	if err := stop(ctx, defaultEnv(), cfg, providerID); err != nil {
		return err
	}
	routeAfterStop(cfg, providerID, "")
	return nil
}
```
```go
func StopModel(ctx context.Context, cfg *config.Config, providerID, modelName string) error {
	if err := stopModel(ctx, defaultEnv(), cfg, providerID, modelName); err != nil {
		return err
	}
	routeAfterStop(cfg, providerID, modelName)
	return nil
}
```
Update the `Stop` doc comment (drop "wt has no user-facing stop command") and add one sentence to each: "After a successful … the model's LiteLLM route is updated (routes.go)."

- [ ] **Step 4: Add a lifecycle-package TestMain guard**

If `wt/internal/lifecycle` has no `TestMain`, add one in `routes_test.go` so no test in the package can reach the real config.yaml or proxy through the public wrappers:

```go
func TestMain(m *testing.M) {
	applyRoutes = func(*config.Config, []string, []string, litellm.Options) (litellm.Result, error) {
		return litellm.Result{}, errors.New("applyRoutes not stubbed in this test")
	}
	os.Exit(m.Run())
}
```
(add `"os"` to imports; if a `TestMain` already exists in the package, add the assignment there instead.)

- [ ] **Step 5: Run — expect PASS; then the whole gate**

Run: `cd wt && go test ./internal/lifecycle -run Route -v && go build ./... && go vet ./... && go test ./... 2>&1 | tail -15`

- [ ] **Step 6: Commit**

```bash
git add wt/internal/lifecycle
git commit -m "fix(wt): keep LiteLLM routes in sync on model start/stop — completes plan item #5

Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>"
```

---

### Task 6: Verify every start/stop path and reproduce the original bug end to end

**Files:**
- Modify: `wt/CLAUDE.md` (lines ~197 and ~232: the "wt never restarts the proxy" / "modelman start keeps exposed in sync" statements), `docs/superpowers/specs/2026-09-20-wt-lifecycle-engine-design.md` (add a dated note under the "LiteLLM config" limitation lines 24–26).
- Test: none new (the hook lives in the public wrappers all paths already call).

- [ ] **Step 1: Prove no start/stop path bypasses the wrappers**

Run:
```bash
cd wt && grep -rn 'lifecycle\.\(Start\|Stop\|StopModel\)\b' internal cmd --include='*.go' | grep -v _test
```
Expected: exactly `cmd/wt/start.go:27` (`lifecycleStart = lifecycle.Start`), `internal/tui/start_flow.go:17` (`startModel = lifecycle.Start`), and `internal/survey/stop.go:39` (`stop: lifecycle.StopModel`). If any other call site appears, or one calls the lowercase `start`/`stop`, route it through the public wrapper.

- [ ] **Step 2: Reproduce the original failure and confirm the fix (manual, live)**

With the MTPLX server stopped (`wt stop mtplx`) and LiteLLM running:
```bash
cd wt && make install     # installs the new wt
wt start 'mtplx/Youssofal--Qwen3.6-35B-A3B-MTPLX-Optimized-Balance'
K=$(grep -A2 '^general_settings' ~/.config/litellm/config.yaml | awk '/master_key/{print $2}')
curl -s localhost:4000/v1/models -H "Authorization: Bearer $K" | python3 -c "import sys,json;print([m['id'] for m in json.load(sys.stdin)['data'] if 'mtplx' in m['id']])"
```
Expected: the list contains `mtplx/Youssofal--Qwen3.6-35B-A3B-MTPLX-Optimized-Balance`. Then `wt stop mtplx` and re-run the curl: the id is gone. (Do not print `$K`.) Finally run `wt litellm sync` and confirm the stale `mtplx/Youssofal/Qwen3.8-27B-MTPLX-Optimized-Quality` route is removed.

- [ ] **Step 3: Update docs**

In `wt/CLAUDE.md`, replace the sentence "wt never starts, stops, or restarts the proxy" with: "wt manages LiteLLM's `config.yaml` routes and restarts the proxy after a change (`internal/litellm`, `wt litellm ...`); it never starts the proxy from cold." Replace "`modelman start` keeps `exposed` in sync automatically…" with a pointer to the lifecycle route hook. Add a `internal/litellm/` row to the package table and a `wt litellm` row to the commands table. In the lifecycle-engine spec, add under the limitation: "Superseded 2026-09-21: `lifecycle.Start/Stop/StopModel` now update LiteLLM routes — see `2026-09-21-wt-litellm-ownership-design.md`."

- [ ] **Step 4: Run repo checks**

Run: `make check-links && cd wt && go test ./... 2>&1 | tail -5`

- [ ] **Step 5: Commit — end of Phase 2**

```bash
git add wt/CLAUDE.md docs/superpowers
git commit -m "docs(wt): LiteLLM routes now follow wt start/stop — completes plan item #6

Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>"
```

**Phase 2 checkpoint:** the reported bug is fixed. Stop and ask the user before pushing or opening a PR.

---

# PHASE 3 — `[litellm]` routing state moves into wt

### Task 7: `[litellm]` in wt's `config.toml`, with one-time migration

**Files:**
- Modify: `wt/internal/config/config.go` (Config struct ~294, `Load` ~347–392, `finalizeCfg` ~394, `Save` ~737), `wt/internal/config/modelman.go` (`modelmanState`, `loadModelmanState`)
- Test: `wt/internal/config/litellm_state_test.go`

**Interfaces:**
- Produces: `Config.LitellmTable *LitellmState` (toml `litellm,omitempty`); `(*Config).UpdateLitellm(mutate func(*LitellmState)) error`; `(*Config).LitellmConfigured() bool`.
- Behavior: precedence = wt `config.toml [litellm]` › legacy `modelman.toml [litellm]` (read fallback + one-time copy into config.toml **only when config.toml already exists**).

- [ ] **Step 1: Write the failing tests**

`wt/internal/config/litellm_state_test.go` (follow the environment helpers used by neighboring tests such as `modelman_test.go` — set `XDG_CONFIG_HOME` to a temp dir, write `local-ai/registry.toml` and optional `agent-wt/config.toml`/`local-ai/modelman.toml`; reuse whichever existing helper does that):

```go
package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func litellmStateEnv(t *testing.T, wtToml, modelmanToml string) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", home)
	t.Setenv("MODELMAN_REGISTRY", "")
	must := func(rel, body string) {
		p := filepath.Join(home, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	must("local-ai/registry.toml", "providers = []\nmodels = []\n")
	if wtToml != "" {
		must("agent-wt/config.toml", wtToml)
	}
	if modelmanToml != "" {
		must("local-ai/modelman.toml", modelmanToml)
	}
	return home
}

const legacyLitellm = "[litellm]\nenabled = true\nurl = \"http://localhost:4000\"\napi_key = \"sk-legacy\"\n"

// TestLitellmStateMigratesFromModelmanOnce pins the one-time migration: when
// wt's config.toml has no [litellm] but modelman.toml does, Load copies it
// into config.toml (so wt owns it from then on), and a second Load reads wt's
// copy. Without it, users would silently lose their proxy URL/key on upgrade.
func TestLitellmStateMigratesFromModelmanOnce(t *testing.T) {
	home := litellmStateEnv(t, "default_tag = \"code\"\n", legacyLitellm)
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.IsLitellm() || cfg.LitellmBaseURL() != "http://localhost:4000" || cfg.LitellmAPIKey() != "sk-legacy" {
		t.Fatalf("legacy state not read: %+v", cfg.litellm)
	}
	b, _ := os.ReadFile(filepath.Join(home, "agent-wt", "config.toml"))
	if !strings.Contains(string(b), "[litellm]") || !strings.Contains(string(b), "sk-legacy") {
		t.Fatalf("state not persisted to config.toml:\n%s", b)
	}
	// Owner is now wt: changing modelman's copy must have no effect.
	os.WriteFile(filepath.Join(home, "local-ai", "modelman.toml"), []byte("[litellm]\nenabled = false\n"), 0o644)
	cfg2, _ := Load()
	if !cfg2.IsLitellm() {
		t.Fatal("wt's own [litellm] must win over modelman.toml after migration")
	}
}

// TestLitellmStateNoConfigTomlDoesNotCreateIt pins the safety guard: when
// config.toml does not exist yet, Load must not create it just to persist a
// migration (that would pre-empt agent seeding); it falls back to modelman's
// values in memory.
func TestLitellmStateNoConfigTomlDoesNotCreateIt(t *testing.T) {
	home := litellmStateEnv(t, "", legacyLitellm)
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.IsLitellm() {
		t.Fatal("fallback to modelman.toml not applied")
	}
	if _, err := os.Stat(filepath.Join(home, "agent-wt", "config.toml")); !os.IsNotExist(err) {
		t.Fatalf("config.toml was created by Load (err=%v)", err)
	}
}

// TestUpdateLitellmPersists pins the setter used by `wt litellm on/off/set`:
// it updates memory and config.toml, and writes the file 0600 when it holds an
// api_key (the LiteLLM master key must not be world-readable).
func TestUpdateLitellmPersists(t *testing.T) {
	home := litellmStateEnv(t, "default_tag = \"code\"\n", "")
	cfg, _ := Load()
	if err := cfg.UpdateLitellm(func(s *LitellmState) { s.Enabled, s.URL, s.APIKey = true, "http://x:4000/", "sk-new" }); err != nil {
		t.Fatal(err)
	}
	if !cfg.IsLitellm() || cfg.LitellmBaseURL() != "http://x:4000" {
		t.Fatalf("in-memory not updated: %+v", cfg.litellm)
	}
	p := filepath.Join(home, "agent-wt", "config.toml")
	st, _ := os.Stat(p)
	if st.Mode().Perm() != 0o600 {
		t.Fatalf("config.toml mode = %v, want 0600 when it holds an api_key", st.Mode().Perm())
	}
	cfg2, _ := Load()
	if cfg2.LitellmAPIKey() != "sk-new" {
		t.Fatal("state did not round-trip through config.toml")
	}
}
```

- [ ] **Step 2: Run — expect FAIL**

Run: `cd wt && go test ./internal/config -run Litellm 2>&1 | head`

- [ ] **Step 3: Implement**

In `Config` add: `LitellmTable *LitellmState \`toml:"litellm,omitempty"\`` (wt-owned persisted state) and unexported `migratedLitellm bool \`toml:"-"\``. Update the `LitellmState` doc comment: "wt-owned since 2026-09-21; mirrors `[litellm]` in wt's config.toml. `modelman.toml`'s `[litellm]` is a legacy read-only fallback."

`modelmanState.Litellm` becomes `Litellm *LitellmState \`toml:"litellm"\``; change `loadModelmanState` to return `(map[string]ExposureEntry, *LitellmState, error)` (nil when the table is absent). Update its two callers.

`finalizeCfg`:

```go
	exposed, legacy, err := loadModelmanState()
	if err != nil {
		return nil, err
	}
	cfg.exposed = exposed
	switch {
	case cfg.LitellmTable != nil:
		cfg.litellm = *cfg.LitellmTable
	case legacy != nil:
		cfg.litellm = *legacy
		cfg.LitellmTable = legacy
		cfg.migratedLitellm = true
	}
	return cfg, nil
```

In `Load`, in the branch that decoded an existing `config.toml`, after `finalizeCfg` returns `c`:

```go
	c, err := finalizeCfg(cfg, providers, models)
	if err != nil {
		return nil, err
	}
	if c.migratedLitellm {
		if err := Save(c); err != nil {
			fmt.Fprintf(os.Stderr, "wt: could not persist [litellm] to config.toml: %v\n", err)
		} else {
			fmt.Fprintln(os.Stderr, "wt: moved [litellm] routing state from modelman.toml into wt's config.toml")
		}
		c.migratedLitellm = false
	}
	return c, nil
```
(The not-exists branch keeps returning `finalizeCfg(...)` without persisting — the guard tested above.)

`Save`: pick the mode by content:

```go
	mode := os.FileMode(0o644)
	if cfg.LitellmTable != nil && cfg.LitellmTable.APIKey != "" {
		mode = 0o600
	}
	return WriteFileAtomic(Path(), buf.Bytes(), mode)
```

Add setter and helper:

```go
// UpdateLitellm applies mutate to the LiteLLM routing state and persists it to
// wt's config.toml. wt owns this state (moved from modelman.toml 2026-09-21).
func (c *Config) UpdateLitellm(mutate func(*LitellmState)) error {
	s := c.litellm
	mutate(&s)
	c.litellm = s
	c.LitellmTable = &s
	return Save(c)
}

// LitellmConfigured reports whether both the proxy URL and API key are set.
func (c *Config) LitellmConfigured() bool { return c.litellm.URL != "" && c.litellm.APIKey != "" }
```

- [ ] **Step 4: Run — expect PASS; fix ripple**

Run: `cd wt && go build ./... && go vet ./... && go test ./... 2>&1 | tail -20`. Any test that constructed `modelmanState` or called `loadModelmanState` with the old 3-value shape needs the pointer return; update them (tests only). The `docs/contracts/modelman.sample.toml` still carries `[litellm]` — keep it (it documents the legacy fallback) and update its header comment to say wt now owns the table and reads modelman's only as a fallback.

- [ ] **Step 5: Commit**

```bash
git add wt docs/contracts
git commit -m "feat(wt): own [litellm] routing state in config.toml with one-time migration — completes plan item #7

Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>"
```

---

### Task 8: `wt litellm status|on|off|set`

**Files:**
- Modify: `wt/cmd/wt/litellm.go`, `wt/cmd/wt/litellm_test.go`

**Interfaces:**
- Consumes: `(*Config).UpdateLitellm`, `IsLitellm`, `LitellmBaseURL`, `LitellmAPIKey`, `LitellmConfigured`.
- Produces: `runLitellmStatus(out io.Writer, cfg *config.Config, asJSON bool) error`, `runLitellmToggle(out, errOut io.Writer, cfg *config.Config, on bool) error`, `runLitellmSet(out io.Writer, cfg *config.Config, url, key *string) error`.

- [ ] **Step 1: Write the failing tests** (append to `litellm_test.go`; use `withCleanConfigEnv(t, home)` + a `config.toml`/registry via `writeEmptyRegistry(t, home)` so `UpdateLitellm`'s `Save` writes into the temp dir)

```go
// TestLitellmStatusAndToggle pins the routing-state commands: `on`/`off` flip
// enabled (routing policy only — never touch the proxy), `set` updates
// url/key independently, status never prints the key (only api_key_set), and
// `on` warns when url/key are unset because agents would fail at launch.
func TestLitellmStatusAndToggle(t *testing.T) {
	home := t.TempDir()
	writeEmptyRegistry(t, home)
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	var out, errOut bytes.Buffer
	if err := runLitellmToggle(&out, &errOut, cfg, true); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(errOut.String(), "litellm.url or litellm.api_key is not set") {
		t.Fatalf("no incomplete-config warning: %q", errOut.String())
	}
	u, k := "http://localhost:4000", "sk-123456"
	if err := runLitellmSet(&out, cfg, &u, &k); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if err := runLitellmStatus(&out, cfg, true); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String(), "sk-123456") {
		t.Fatalf("status leaked the api key: %s", out.String())
	}
	var st struct {
		Enabled   bool   `json:"enabled"`
		URL       string `json:"url"`
		APIKeySet bool   `json:"api_key_set"`
	}
	if err := json.Unmarshal(out.Bytes(), &st); err != nil || !st.Enabled || st.URL != u || !st.APIKeySet {
		t.Fatalf("status = %s (%v)", out.String(), err)
	}
	if err := runLitellmToggle(&out, &errOut, cfg, false); err != nil || cfg.IsLitellm() {
		t.Fatalf("off failed: err=%v enabled=%v", err, cfg.IsLitellm())
	}
}
```

- [ ] **Step 2: Run — expect FAIL**, then implement in `litellm.go`:

```go
func runLitellmStatus(out io.Writer, cfg *config.Config, asJSON bool) error {
	if asJSON {
		return json.NewEncoder(out).Encode(map[string]any{
			"enabled": cfg.IsLitellm(), "url": cfg.LitellmBaseURL(), "api_key_set": cfg.LitellmAPIKey() != "",
		})
	}
	mode := "off"
	if cfg.IsLitellm() {
		mode = "on"
	}
	key := "(unset)"
	if k := cfg.LitellmAPIKey(); k != "" {
		key = "***" + k[max(0, len(k)-4):]
	}
	url := cfg.LitellmBaseURL()
	if url == "" {
		url = "(unset)"
	}
	fmt.Fprintf(out, "litellm: %s\n  url: %s\n  api_key: %s\n", mode, url, key)
	return nil
}

func runLitellmToggle(out, errOut io.Writer, cfg *config.Config, on bool) error {
	if err := cfg.UpdateLitellm(func(s *config.LitellmState) { s.Enabled = on }); err != nil {
		return err
	}
	if on {
		fmt.Fprintln(out, "litellm: on")
		if !cfg.LitellmConfigured() {
			fmt.Fprintln(errOut, "warning: litellm.url or litellm.api_key is not set — wt will fail at launch time; run 'wt litellm set --url ... --api-key ...'")
		}
		return nil
	}
	fmt.Fprintln(out, "litellm: off")
	if !cfg.LitellmConfigured() {
		fmt.Fprintln(errOut, "warning: litellm.url or litellm.api_key is not set — agents whose protocols force LiteLLM will still fail to launch")
	}
	return nil
}

func runLitellmSet(out io.Writer, cfg *config.Config, url, key *string) error {
	if err := cfg.UpdateLitellm(func(s *config.LitellmState) {
		if url != nil {
			s.URL = *url
		}
		if key != nil {
			s.APIKey = *key
		}
	}); err != nil {
		return err
	}
	fmt.Fprintln(out, "litellm: updated")
	return nil
}
```

Register in `litellmCmd`: `status [--json]`, `on`, `off`, and `set` with string flags `--url` and `--api-key` (`cmd.Flags().Changed("url")` decides whether to pass a non-nil pointer).

- [ ] **Step 3: Run gate, commit — end of Phase 3**

```bash
cd wt && go build ./... && go vet ./... && go test ./... 2>&1 | tail -10
git add wt && git commit -m "feat(wt): wt litellm status|on|off|set — completes plan item #8

Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>"
```

**Phase 3 checkpoint:** stop and ask the user before pushing or opening a PR. Phase 4 requires the new `wt` binary installed (`make install`).

---

# PHASE 4 — modelman delegates to wt

> **Pre-flight amendments (2026-09-21, after Phases 1-3 merged; these override the task text below where they conflict):**
> 1. **New Task 9a (Go, runs first):** `wt litellm expose|unexpose|sync` must gate on `app.loadErr` (config failed to load) instead of the full `cfgErr` (which includes registry *validation* gaps). Once modelman delegates, a data gap elsewhere in the registry must not block modelman — the tool that repairs such gaps. `Apply` already validates the specific model per id.
> 2. **Task 10 ordering:** `queue.py::apply` saves `registry.toml` only in `_persist` at the end, AFTER `apply_expose_queue`. wt reads the registry from disk, so `save_registry(self.registry, self.registry_path)` must run before the expose step (a failure reports the expose batch as failed). Re-check `_expose_for_start` and the stop paths in `local_control.py` for the same ordering; `_register_discovered_model` already saves before exposing.
> 3. **Task 10 display paths:** `provider_cloud_flags()`/`provider_policy()`/`is_cloud()` are reached from TUI render code for every row, so they must not raise when `wt` is missing or fails: return `{}` and warn once on stderr. Write paths (`expose_model` etc.) still raise `LiteLLMConfigError` before any state change.
> 4. **Task 11 state.py:** instead of keeping a modeled `LitellmState` as an "inert passthrough", remove the `litellm` field from `StateStore`; the raw `[litellm]` table (if present) stays in `StateStore.extra` and is written back verbatim, and nothing is written when it is absent. This removes the api_key round-trip hazard and stops modelman creating a default `[litellm]` table. `migrate_wt_gateway_to_litellm` keeps working on `state.extra["litellm"]`.
> 5. **Task 11 text:** `modelman litellm status` passes through wt's TEXT output (the `***last4` masking lives there; `status --json` only has `api_key_set`); the TUI status line reads `wt_bridge.litellm_status()` and shows "LiteLLM: unavailable" if wt cannot be reached.
> 6. **Task 12:** also removes the interim-window notes added in Phases 3 (wt/CLAUDE.md, modelman/CLAUDE.md) and covers docs/guides 00/04/06/07/08/09 and docs/wt-agents/*.md.


### Task 9: `wt_bridge.py` and hermetic test guard

**Files:**
- Create: `modelman/src/modelman/wt_bridge.py`, `modelman/tests/test_wt_bridge.py`, `modelman/tests/contracts/test_litellm_cli_fixture.py`
- Modify: `modelman/tests/conftest.py`

**Interfaces:**
- Produces:
  - `class WtBridgeError(Exception)`, `class WtNotFoundError(WtBridgeError)`
  - `@dataclass(frozen=True) BridgeOutcome(id: str, action: str | None, error: str | None)`; `BridgeResult(outcomes: list[BridgeOutcome], changed: bool, warnings: list[str])`
  - `expose(ids: list[str], *, litellm_path: Path | None = None, skip_ready_gate: bool = True) -> BridgeResult`
  - `unexpose(ids: list[str], *, litellm_path: Path | None = None) -> BridgeResult`
  - `routed_ids(*, litellm_path: Path | None = None) -> list[str]`
  - `provider_cloud_flags() -> dict[str, bool]` (cached per process)
  - `litellm_status() -> LitellmStatus(enabled: bool, url: str, api_key_set: bool)`; `litellm_set_enabled(on: bool)`; `litellm_set(url: str | None, api_key: str | None)`
  - `ensure_wt() -> None` (raises `WtNotFoundError`)

- [ ] **Step 1: Write the failing tests**

`modelman/tests/test_wt_bridge.py`:

```python
"""Tests for the wt subprocess bridge (wt owns LiteLLM management)."""

import json
import subprocess
from pathlib import Path

import pytest

from modelman import wt_bridge


def _cp(stdout="", returncode=0, stderr=""):
    return subprocess.CompletedProcess(args=[], returncode=returncode, stdout=stdout, stderr=stderr)


@pytest.fixture
def calls(monkeypatch):
    """Record argv/env of every wt invocation and return canned output."""
    seen = []
    state = {"out": _cp(json.dumps({"outcomes": [], "changed": False, "warnings": []}))}

    def fake_run(argv, **kwargs):
        seen.append((argv, kwargs.get("env") or {}))
        return state["out"]

    monkeypatch.setattr(wt_bridge, "_run", lambda args, env=None: fake_run(["wt", "litellm", *args], env=env))
    return seen, state


def test_expose_builds_argv_and_parses_json(calls):
    # Pins the wire contract with `wt litellm expose`: modelman always passes
    # --skip-ready-gate (it applied the gate against its own in-memory state)
    # and --json, and per-id errors surface as BridgeOutcome.error rather than
    # exceptions so the queue can report them per model.
    seen, state = calls
    state["out"] = _cp(
        json.dumps({"outcomes": [{"id": "a", "action": "exposed"}, {"id": "b", "error": "nope"}], "changed": True, "warnings": ["w"]}),
        returncode=1,
    )
    res = wt_bridge.expose(["a", "b"])
    assert seen[0][0] == ["wt", "litellm", "expose", "--json", "--skip-ready-gate", "a", "b"]
    assert [(o.id, o.action, o.error) for o in res.outcomes] == [("a", "exposed", None), ("b", None, "nope")]
    assert res.changed and res.warnings == ["w"]


def test_litellm_path_is_passed_via_env(calls):
    # Tests and custom setups point modelman at a non-default config.yaml;
    # the bridge must forward that to wt through WT_LITELLM_CONFIG or wt would
    # edit the developer's real file.
    seen, _ = calls
    wt_bridge.unexpose(["a"], litellm_path=Path("/tmp/x.yaml"))
    assert seen[0][1]["WT_LITELLM_CONFIG"] == "/tmp/x.yaml"


def test_file_level_failure_raises(calls):
    # A non-JSON failure (missing/invalid config.yaml) means nothing applied:
    # the bridge must raise so callers treat the whole batch as failed rather
    # than parsing garbage.
    _, state = calls
    state["out"] = _cp("", returncode=1, stderr="LiteLLM config not found: /x")
    with pytest.raises(wt_bridge.WtBridgeError, match="LiteLLM config not found"):
        wt_bridge.expose(["a"])


def test_missing_wt_binary_is_a_clear_error(monkeypatch):
    # modelman now requires the wt binary; fail before any state change with
    # an actionable message instead of a bare FileNotFoundError.
    monkeypatch.setattr(wt_bridge.shutil, "which", lambda _: None)
    with pytest.raises(wt_bridge.WtNotFoundError, match="make install"):
        wt_bridge.ensure_wt()
```

(The suite-wide `_never_call_real_wt` guard added in Step 4 replaces `_run`, so this test targets `ensure_wt` — the function `_run` calls first — rather than `_run` itself.)

`modelman/tests/contracts/test_litellm_cli_fixture.py`:

```python
"""Cross-language contract: the wt `litellm --json` shapes modelman parses.

Read together with wt/cmd/wt/litellm_test.go; both consume
docs/contracts/litellm-cli.sample.json, so a schema change on either side
fails both CI jobs in the same PR."""

import json
from pathlib import Path

from modelman.wt_bridge import parse_change_result, parse_status

FIXTURE = Path(__file__).resolve().parents[3] / "docs" / "contracts" / "litellm-cli.sample.json"


def test_change_fixture_parses():
    # Pins that every key wt emits for expose/unexpose/sync is understood by
    # the bridge, including per-id errors and restart warnings.
    doc = json.loads(FIXTURE.read_text())
    res = parse_change_result(json.dumps(doc["change"]))
    assert [o.id for o in res.outcomes] == ["ollama/gemma:9b", "claude/sonnet"]
    assert res.outcomes[1].error and res.changed and res.warnings


def test_status_fixture_parses():
    # Pins the routing-state shape modelman's TUI and CLI read.
    doc = json.loads(FIXTURE.read_text())
    st = parse_status(json.dumps(doc["status"]))
    assert st.enabled and st.url == "http://localhost:4000" and st.api_key_set
```

- [ ] **Step 2: Run — expect FAIL** (`ModuleNotFoundError: modelman.wt_bridge`)

Run: `cd modelman && uv run pytest tests/test_wt_bridge.py tests/contracts/test_litellm_cli_fixture.py -q`

- [ ] **Step 3: Implement**

`modelman/src/modelman/wt_bridge.py`:

```python
"""Subprocess bridge to `wt litellm ...`.

wt owns LiteLLM management (config.yaml routes, proxy restart, routing
state); modelman calls these primitives instead of keeping its own copy of
the logic. See docs/superpowers/specs/2026-09-21-wt-litellm-ownership-design.md.
"""

from __future__ import annotations

import functools
import json
import os
import shutil
import subprocess
from dataclasses import dataclass
from pathlib import Path


class WtBridgeError(Exception):
    """wt could not process the request (nothing was changed)."""


class WtNotFoundError(WtBridgeError):
    """The wt binary is not on PATH."""


@dataclass(frozen=True)
class BridgeOutcome:
    id: str
    action: str | None
    error: str | None


@dataclass(frozen=True)
class BridgeResult:
    outcomes: list[BridgeOutcome]
    changed: bool
    warnings: list[str]


@dataclass(frozen=True)
class LitellmStatus:
    enabled: bool
    url: str
    api_key_set: bool


def ensure_wt() -> None:
    """Raise WtNotFoundError unless the wt binary is on PATH."""
    if shutil.which("wt") is None:
        raise WtNotFoundError("wt not found on PATH; install it with `make install`")


def _run(args: list[str], env: dict[str, str] | None = None) -> subprocess.CompletedProcess[str]:
    ensure_wt()
    return subprocess.run(
        ["wt", "litellm", *args],
        capture_output=True,
        text=True,
        timeout=120,
        env={**os.environ, **(env or {})},
    )


def _env(litellm_path: Path | None) -> dict[str, str]:
    return {"WT_LITELLM_CONFIG": str(litellm_path)} if litellm_path is not None else {}


def parse_change_result(stdout: str) -> BridgeResult:
    doc = json.loads(stdout)
    return BridgeResult(
        outcomes=[
            BridgeOutcome(o["id"], o.get("action") or None, o.get("error") or None)
            for o in doc.get("outcomes", [])
        ],
        changed=bool(doc.get("changed")),
        warnings=list(doc.get("warnings") or []),
    )


def parse_status(stdout: str) -> LitellmStatus:
    doc = json.loads(stdout)
    return LitellmStatus(bool(doc["enabled"]), doc.get("url") or "", bool(doc["api_key_set"]))


def _change(args: list[str], litellm_path: Path | None) -> BridgeResult:
    proc = _run(args, _env(litellm_path))
    try:
        return parse_change_result(proc.stdout)
    except (ValueError, KeyError):
        # No JSON on stdout: a file-level failure (missing/invalid config.yaml,
        # unreadable registry). Nothing was applied.
        raise WtBridgeError((proc.stderr or proc.stdout).strip() or f"wt exited {proc.returncode}") from None


def expose(
    ids: list[str], *, litellm_path: Path | None = None, skip_ready_gate: bool = True
) -> BridgeResult:
    args = ["expose", "--json"] + (["--skip-ready-gate"] if skip_ready_gate else []) + ids
    return _change(args, litellm_path)


def unexpose(ids: list[str], *, litellm_path: Path | None = None) -> BridgeResult:
    return _change(["unexpose", "--json", *ids], litellm_path)


def routed_ids(*, litellm_path: Path | None = None) -> list[str]:
    proc = _run(["list", "--json"], _env(litellm_path))
    try:
        return list(json.loads(proc.stdout)["routed"])
    except (ValueError, KeyError):
        raise WtBridgeError((proc.stderr or proc.stdout).strip() or "wt litellm list failed") from None


@functools.cache
def provider_cloud_flags() -> dict[str, bool]:
    """provider id -> is cloud, for every LiteLLM-mapped provider (wt owns the table)."""
    proc = _run(["providers", "--json"])
    try:
        return {pid: bool(v["cloud"]) for pid, v in json.loads(proc.stdout)["providers"].items()}
    except (ValueError, KeyError):
        raise WtBridgeError((proc.stderr or proc.stdout).strip() or "wt litellm providers failed") from None


def litellm_status() -> LitellmStatus:
    proc = _run(["status", "--json"])
    try:
        return parse_status(proc.stdout)
    except (ValueError, KeyError):
        raise WtBridgeError((proc.stderr or proc.stdout).strip() or "wt litellm status failed") from None


def litellm_set_enabled(on: bool) -> None:
    proc = _run(["on" if on else "off"])
    if proc.returncode != 0:
        raise WtBridgeError((proc.stderr or proc.stdout).strip())


def litellm_set(url: str | None, api_key: str | None) -> None:
    args = ["set"] + (["--url", url] if url is not None else []) + (["--api-key", api_key] if api_key is not None else [])
    proc = _run(args)
    if proc.returncode != 0:
        raise WtBridgeError((proc.stderr or proc.stdout).strip())
```

- [ ] **Step 4: Add the autouse guard** to `modelman/tests/conftest.py` (next to `_never_restart_live_proxy`):

```python
@pytest.fixture(autouse=True)
def _never_call_real_wt(monkeypatch):
    """The suite must never run the real `wt` binary: it would rewrite the
    developer's real LiteLLM config.yaml and bounce their live proxy. Every
    bridge call goes through wt_bridge._run; replace it with a fake that
    applies exposes to nothing and reports the known provider table.
    Tests that assert on the bridge itself monkeypatch _run again."""
    import json
    import subprocess

    def fake(args, env=None):
        if args[:1] == ["providers"]:
            out = {"providers": {p: {"cloud": p == "openrouter"} for p in ("ollama", "omlx", "mlx_lm_server", "mtplx", "llamacpp", "openrouter")}}
        elif args[:1] in (["list"],):
            out = {"routed": []}
        else:
            ids = [a for a in args[1:] if not a.startswith("--")]
            act = "unexposed" if args[:1] == ["unexpose"] else "exposed"
            out = {"outcomes": [{"id": i, "action": act} for i in ids], "changed": True, "warnings": []}
        return subprocess.CompletedProcess(args=[], returncode=0, stdout=json.dumps(out), stderr="")

    monkeypatch.setattr("modelman.wt_bridge._run", fake)
    monkeypatch.setattr("modelman.wt_bridge.provider_cloud_flags.cache_clear", lambda: None, raising=False)
```
(If `functools.cache`'s `cache_clear` patch is awkward, instead call `wt_bridge.provider_cloud_flags.cache_clear()` inside the fixture before yielding.)

- [ ] **Step 5: Run — expect PASS**; commit

```bash
cd modelman && uv run pytest tests/test_wt_bridge.py tests/contracts -q && make check
git add modelman docs && git commit -m "feat(modelman): wt_bridge subprocess wrapper for wt litellm — completes plan item #9

Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>"
```

---

### Task 10: Delegate expose/unexpose/queues to wt; delete the Python write path

**Files:**
- Modify: `modelman/src/modelman/litellm.py`, `modelman/tests/test_litellm.py`, `test_expose.py`, `test_queue.py`, `test_litellm_cli.py`
- Verify: `modelman/src/modelman/queue.py` (~615-660), `local_control.py` (~575-640, ~807-880, ~1118-1150)

**Interfaces:**
- Consumes: `wt_bridge.expose/unexpose`, `wt_bridge.WtBridgeError`.
- Keep signatures: `expose_model(registry, state, model_id, litellm_path) -> list[str]`, `unexpose_model(state, model_id, litellm_path) -> list[str]`, `apply_expose_queue(registry, state, exposes, litellm_path) -> (outcomes, warnings)`, `apply_unexpose_queue(state, model_ids, litellm_path) -> list[str]`. Exceptions: `ExposeError`, `LiteLLMConfigError` unchanged.
- Keep read-only helpers used elsewhere: `load_litellm_config`, `_database_url_from_config`, `_reverse_model_index`, `default_litellm_config_path`, `is_effectively_exposed`, `passes_ready_gate`, `is_cloud`, `is_cloud_effective`, `ExposeError`, `LiteLLMConfigError`.
- Delete: `build_model_list_entry`, `_pricing_model_info`, `set_exposed`, `remove_exposed`, `ensure_litellm_settings`, `save_litellm_config`, `_add_presence_based_param`, `_is_local_api_base`, the ruamel *write* path, `restart_litellm_proxy`, `default_litellm_restart_cmd`, `_FALLBACK_LITELLM_RESTART_CMD`, `_validated_entry`'s `build_model_list_entry` call. `PROVIDER_POLICIES`/`provider_policy`/`ProviderPolicy` are replaced by `wt_bridge.provider_cloud_flags()` (see below).

- [ ] **Step 1: Verify the disk-ordering hazard (must pass before deleting anything)**

wt reads `registry.toml` and `ready` from disk; modelman previously used in-memory objects. Confirm every call site has already persisted what wt needs:

```bash
cd modelman/src/modelman
grep -n "save_registry\|apply_expose_queue\|self.state.save\|save_state\|expose_model(" queue.py local_control.py main.py | head -40
```
For each of `queue.py` (apply), `local_control._register_discovered_model`, `_expose_for_start`, and `main.py` expose/unexpose commands: the registry save (`save_registry`) for any model being exposed must happen **before** the bridge call. Write a failing test per site that saves nothing before exposing a freshly-registered model and asserts the bridge is invoked only after `save_registry` (use a recording fake for both). If a site exposes first, reorder it (registry save first, then expose, then flags) — this is the only behavior change permitted in this task.

- [ ] **Step 2: Write failing tests for the delegating functions** (replace the Python-write tests in `test_expose.py`/`test_litellm.py` with these; port the row-shape, settings-enforcement, comment-preservation and idempotence cases — they now live in Go, Tasks 1–3)

```python
def test_expose_model_delegates_and_flips_flag_after_success(monkeypatch, tmp_path):
    # Pins ordering: the exposed flag flips only after wt reports the route was
    # written, so state never claims an exposure config.yaml lost. Also pins
    # that modelman keeps its own ready gate (wt is told --skip-ready-gate).
    calls = []
    monkeypatch.setattr(
        wt_bridge, "expose",
        lambda ids, **kw: calls.append((ids, kw)) or wt_bridge.BridgeResult(
            [wt_bridge.BridgeOutcome(ids[0], "exposed", None)], True, ["restart warning"]),
    )
    registry, state = make_ready_local_model("ollama/a:1")
    warnings = expose_model(registry, state, "ollama/a:1", tmp_path / "config.yaml")
    assert calls == [(["ollama/a:1"], {"litellm_path": tmp_path / "config.yaml", "skip_ready_gate": True})]
    assert state.get("ollama/a:1").exposed is True
    assert warnings == ["restart warning"]


def test_expose_model_leaves_flag_when_wt_rejects(monkeypatch, tmp_path):
    # If wt rejects the id (or the bridge raises), the flag must stay false and
    # ExposeError/LiteLLMConfigError must surface as before.
    monkeypatch.setattr(wt_bridge, "expose", lambda ids, **kw: wt_bridge.BridgeResult(
        [wt_bridge.BridgeOutcome(ids[0], None, "provider has no LiteLLM mapping")], False, []))
    registry, state = make_ready_local_model("ollama/a:1")
    with pytest.raises(ExposeError, match="no LiteLLM mapping"):
        expose_model(registry, state, "ollama/a:1", tmp_path / "config.yaml")
    assert state.get("ollama/a:1").exposed is False
```
(`make_ready_local_model` = a small helper building a `Registry` + `StateStore` with a ready ollama model; reuse the fixtures already in `test_expose.py`.) Add analogous tests for `unexpose_model`, `apply_expose_queue` (mixed queue → one `expose` call and one `unexpose` call; per-id error → `(id, target, message)` tuple and flag untouched; `WtBridgeError` → propagates as `LiteLLMConfigError`), and `apply_unexpose_queue` (one call, flags cleared after success).

- [ ] **Step 3: Run — expect FAIL**; then implement

New bodies in `litellm.py` (add `from . import wt_bridge`):

```python
def _validate_locally(registry: Registry, state: StateStore, model_id: str) -> None:
    """The gates modelman applies against its own in-memory state (which may
    hold unsaved ready/exposed toggles wt cannot see): model exists, provider
    exists, not native, provider has a LiteLLM mapping, ready or cloud."""
    try:
        model = registry.model(model_id)
    except KeyError:
        raise ExposeError(f"model {model_id!r} not found in registry") from None
    try:
        registry.provider(model.provider_id)
    except KeyError:
        raise ExposeError(
            f"model {model_id!r} references unknown provider {model.provider_id!r}"
        ) from None
    if model.native:
        raise ExposeError(
            f"provider {model.provider_id!r} is native and cannot be exposed through LiteLLM"
        )
    if provider_policy(model.provider_id) is None:
        raise ExposeError(f"provider {model.provider_id!r} has no LiteLLM mapping")
    if not passes_ready_gate(model, state, registry):
        raise ExposeError(f"model {model_id!r} is not ready")


def _bridge(call, *args, **kwargs):
    try:
        return call(*args, **kwargs)
    except wt_bridge.WtBridgeError as exc:
        raise LiteLLMConfigError(str(exc)) from None


def expose_model(registry, state, model_id, litellm_path) -> list[str]:
    """Expose a model: validate against modelman's in-memory state, have wt write the
    route and restart the proxy, then flip the modelman.toml flag."""
    _validate_locally(registry, state, model_id)
    res = _bridge(wt_bridge.expose, [model_id], litellm_path=litellm_path, skip_ready_gate=True)
    if res.outcomes and res.outcomes[0].error:
        raise ExposeError(res.outcomes[0].error)
    _set_exposed_flag(state, model_id, True)
    return res.warnings


def unexpose_model(state, model_id, litellm_path) -> list[str]:
    res = _bridge(wt_bridge.unexpose, [model_id], litellm_path=litellm_path)
    _set_exposed_flag(state, model_id, False)
    return res.warnings


def apply_expose_queue(registry, state, exposes, litellm_path):
    if not exposes:
        return [], []
    outcomes: list[tuple[str, bool, str | None]] = []
    to_add: list[str] = []
    to_remove: list[str] = []
    for model_id, target in exposes:
        if not target:
            to_remove.append(model_id)
            continue
        try:
            _validate_locally(registry, state, model_id)
        except ExposeError as exc:
            outcomes.append((model_id, target, str(exc)))
            continue
        to_add.append(model_id)
    warnings: list[str] = []
    errors: dict[str, str] = {}
    if to_add:
        res = _bridge(wt_bridge.expose, to_add, litellm_path=litellm_path, skip_ready_gate=True)
        warnings += res.warnings
        errors.update({o.id: o.error for o in res.outcomes if o.error})
    if to_remove:
        res = _bridge(wt_bridge.unexpose, to_remove, litellm_path=litellm_path)
        warnings += res.warnings
    for model_id, target in exposes:
        if any(o[0] == model_id for o in outcomes):
            continue  # already failed local validation
        if model_id in errors:
            outcomes.append((model_id, target, errors[model_id]))
            continue
        outcomes.append((model_id, target, None))
        _set_exposed_flag(state, model_id, target)
    return outcomes, warnings


def apply_unexpose_queue(state, model_ids, litellm_path) -> list[str]:
    if not model_ids:
        return []
    res = _bridge(wt_bridge.unexpose, model_ids, litellm_path=litellm_path)
    for model_id in model_ids:
        _set_exposed_flag(state, model_id, False)
    return res.warnings
```

Replace `PROVIDER_POLICIES`/`ProviderPolicy`/`provider_policy`/`is_cloud` with bridge-backed versions:

```python
@dataclass(frozen=True)
class ProviderPolicy:
    """Minimal view of wt's provider mapping: modelman only needs to know a
    mapping exists and whether the provider is cloud (exempt from the ready gate)."""
    cloud: bool = False


def provider_policy(provider_id: str) -> ProviderPolicy | None:
    flags = wt_bridge.provider_cloud_flags()
    return ProviderPolicy(cloud=flags[provider_id]) if provider_id in flags else None
```
`is_cloud(provider_id)` keeps its body using `provider_policy`. Update the module docstring to say wt owns row building, YAML edits, and restart.

Delete the deleted functions listed above, drop the now-unused imports (`ruamel`, `subprocess`, `copy`, `Callable`, `urlparse`, `Cost`, `_toml_io.atomic_write` if unused), and remove `_rt_yaml` only if `load_litellm_config` no longer needs it — `load_litellm_config` is read-only and stays; keep a read-only `YAML(typ="safe")` load there (comments do not matter for reads) and delete `_rt_yaml`.

- [ ] **Step 4: Delete/port obsolete tests**

Delete Python tests that assert the removed write path (row shape, `ensure_litellm_settings`, `set_exposed`/`remove_exposed`, restart command, ruamel comment preservation). Every such case has a Go counterpart in Tasks 1–3 (`TestBuildEntry*`, `TestEnsureSettings`, `TestSetRow*`, `TestRestart*`). In `conftest.py` remove `_never_restart_live_proxy` and `_default_litellm_config` only if nothing else uses `MODELMAN_LITELLM_RESTART_CMD`/`MODELMAN_LITELLM_CONFIG` (the config-path fixture is still needed by read-only callers; keep it).

- [ ] **Step 5: Run — expect PASS; commit**

```bash
cd modelman && uv run pytest -q && make check
git add modelman && git commit -m "refactor(modelman): delegate LiteLLM config writes and restart to wt — completes plan item #10

Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>"
```

---

### Task 11: Routing-state passthroughs, EXPOSED column, and the inert legacy table

**Files:**
- Modify: `modelman/src/modelman/main.py` (`litellm status|on|off|set`, lines ~54-120), `modelman/src/modelman/screens/models.py` (litellm toggle ~384-401; local EXPOSED display ~447), `modelman/src/modelman/state.py` (~170-180), `modelman/tests/test_litellm_cli.py`
- Test: `modelman/tests/test_litellm_cli.py`, `modelman/tests/test_state.py` (or the existing state test module)

**Interfaces:**
- Consumes: `wt_bridge.litellm_status/litellm_set_enabled/litellm_set/routed_ids`.

- [ ] **Step 1: Write failing tests**

```python
def test_litellm_status_passthrough(monkeypatch):
    # `modelman litellm status` must show wt's state (wt owns [litellm] now),
    # never the legacy modelman.toml table, and must not print the api key.
    monkeypatch.setattr(wt_bridge, "litellm_status", lambda: wt_bridge.LitellmStatus(True, "http://localhost:4000", True))
    result = runner.invoke(app, ["litellm", "status"])
    assert "litellm: on" in result.output and "http://localhost:4000" in result.output
    assert "sk-" not in result.output


def test_litellm_on_off_set_call_wt(monkeypatch):
    # Pins that on/off/set are pure passthroughs: modelman no longer writes
    # [litellm] into modelman.toml, so wt is the only writer.
    seen = []
    monkeypatch.setattr(wt_bridge, "litellm_set_enabled", lambda on: seen.append(("enabled", on)))
    monkeypatch.setattr(wt_bridge, "litellm_set", lambda url, key: seen.append(("set", url, key)))
    runner.invoke(app, ["litellm", "on"])
    runner.invoke(app, ["litellm", "set", "--url", "http://u", "--api-key", "k"])
    assert seen == [("enabled", True), ("set", "http://u", "k")]


def test_state_preserves_legacy_litellm_table_untouched(tmp_path):
    # SAFETY: modelman must keep round-tripping a legacy [litellm] table in
    # modelman.toml verbatim. If wt has not yet migrated it (wt copies it into
    # its own config on first load), dropping it here would lose the user's
    # proxy URL and key — the same ordering trap the old [gateway] migration had.
    p = tmp_path / "modelman.toml"
    p.write_text('[litellm]\nenabled = true\nurl = "http://localhost:4000"\napi_key = "sk-legacy"\n')
    store = load_state(p)
    save_state(store, p)
    assert 'api_key = "sk-legacy"' in p.read_text()
```

- [ ] **Step 2: Run — expect FAIL; implement**

`main.py`: rewrite the four commands as passthroughs over `wt_bridge` (catch `WtBridgeError` → `typer.echo(f"error: {exc}", err=True); raise typer.Exit(1)`); keep the output wording of the old commands. Keep `_warn_if_litellm_incomplete` semantics by reading `wt_bridge.litellm_status()`.

`screens/models.py`: replace the `self.state.litellm` reads (~384-386) with `wt_bridge.litellm_status()` (fetched once per refresh, cached on the screen), and make the `t`-toggle handler (~400) call `wt_bridge.litellm_set_enabled(not status.enabled)` then refresh — no `disk_state.litellm` write. For local models, compute the EXPOSED cell from `set(wt_bridge.routed_ids())` (fetched once per refresh) instead of the flag; cloud/native cells keep `is_effectively_exposed`.

`state.py`: do NOT delete the `litellm` dataclass or its load/save — it is now an **inert passthrough**. Add a comment at the field and at line ~176: "Legacy: wt owns [litellm] since 2026-09-21 (wt/CLAUDE.md). modelman preserves the table verbatim so wt's one-time migration can still read it; modelman never reads or mutates it." Remove any remaining modelman code that mutates `state.litellm` (`migrate.py:81-85` writes it from the old wt `[gateway]` — leave that import path in place, since wt falls back to modelman.toml when its own table is absent, and add a one-line comment saying so).

- [ ] **Step 3: Run gates; commit**

```bash
cd modelman && uv run pytest -q && make check
git add modelman && git commit -m "refactor(modelman): litellm CLI/TUI read and write routing state through wt — completes plan item #11

Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>"
```

---

### Task 12: Docs, drift grep, and full verification

**Files:**
- Modify: `modelman/CLAUDE.md`, `wt/CLAUDE.md`, root `CLAUDE.md`, `docs/guides/00-config-map.md` and the LiteLLM guide (`docs/guides/` — find with `grep -ln litellm docs/guides/*.md`), `docs/superpowers/specs/2026-09-10-litellm-control-design.md` (status banner), `docs/superpowers/specs/2026-09-21-wt-litellm-ownership-design.md` (amendments)

- [ ] **Step 1: Snapshot the exposure-snapshot drift baseline**

Run: `git grep -n "exposed = " docs/guides/ > /tmp/exposed-before.txt`

- [ ] **Step 2: Update docs**

- `docs/superpowers/specs/2026-09-10-litellm-control-design.md`: add a top banner "Ownership split superseded 2026-09-21 by `2026-09-21-wt-litellm-ownership-design.md`; the protocol-negotiation half is unchanged."
- `2026-09-21-wt-litellm-ownership-design.md`: append an **Amendments (plan-time)** section recording: (1) `yaml.v3` reformats sequence indentation and drops blank lines on the first wt write (comments preserved; verified on the live config); (2) exit code `1` also covers partial batches; (3) the `--json` schemas; (4) `wt litellm providers` replaces the Python policy table; (5) `--skip-ready-gate`; (6) the readiness wait after a route change; (7) status → Approved/Implemented.
- `wt/CLAUDE.md`: package table row for `internal/litellm`, `wt litellm` command table, "LiteLLM routing state" section rewritten (wt owns `[litellm]` in `config.toml`; legacy fallback; env vars).
- `modelman/CLAUDE.md`: state that modelman no longer writes `config.yaml` or restarts the proxy; requires `wt` on PATH; `litellm.py` is read-only helpers + delegation; `[litellm]` in modelman.toml is an inert legacy passthrough.
- Root `CLAUDE.md`: architecture bullets for `wt/` and `modelman/`, the LiteLLM config line, and the "exposed snapshots" gotcha (now: local-model exposure = `wt litellm list`).
- `docs/guides/`: update every place that says `modelman litellm on/off/set` is the owner or that a model must be `modelman expose`d before `wt` can use it, so they point at `wt litellm ...` and mention `wt litellm sync`.

- [ ] **Step 3: Full verification (evidence before claims)**

```bash
make lint && make check-links
make test-all
git grep -n "exposed = " docs/guides/ > /tmp/exposed-after.txt; diff /tmp/exposed-before.txt /tmp/exposed-after.txt && echo "no drift"
```
Expected: all green; if the diff shows drift, update the affected guides (`00, 02, 04, 05, 06, 08`).

- [ ] **Step 4: Live end-to-end check (manual)**

1. `modelman start <a local model>` → `wt litellm list --json` contains it and the proxy serves it (modelman → wt bridge path).
2. `wt stop <provider>` → route gone; `modelman` TUI EXPOSED column agrees (`wt litellm list`).
3. `wt litellm off && wt litellm on` → `modelman litellm status` agrees.
4. `wt smoke` for one local model passes.

- [ ] **Step 5: Commit — end of Phase 4**

```bash
git add -A docs modelman wt CLAUDE.md
git commit -m "docs: LiteLLM management now owned by wt — completes plan item #12

Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>"
```

**Phase 4 checkpoint:** present a summary and ask "Ready to create a PR?" for each of the four phase branches.

---

## Self-Review

**Spec coverage**
- `internal/litellm` units (policy, entry, configfile, restart, reconcile) → Tasks 1–3 (`reconcile` = `Apply`/`Sync`).
- CLI `expose|unexpose|sync|list` (+`--dry-run`, `--json`) → Task 4; `status|on|off|set` → Task 8; `providers` (added to replace the Python table) → Task 4.
- wt start/stop/TUI/smoke integration → Tasks 5–6 (single hook in the public lifecycle wrappers, verified against all three call sites).
- `[litellm]` move + migration → Task 7; the no-`config.toml` guard is tested in `TestLitellmStateNoConfigTomlDoesNotCreateIt`. A malformed `[litellm]` table fails the config load like any malformed `config.toml` (the spec was corrected to say so).
- modelman delegation, gate via wt, EXPOSED column from `wt litellm list`, inert legacy table → Tasks 9–11.
- Cross-language contract fixture → Tasks 4 and 9.
- Testing bullets (comment preservation, preserved params, idempotence, native rejection, malformed `model_list`, unreadable file, lock, fake restart, stubbed lifecycle, Python argv/ordering, conftest guards) → Tasks 2, 3, 5, 9, 10.
- Phasing and docs/grep-before-after → Tasks 5/6 (bug fixed) and 12.

**Placeholder scan:** none of "TBD/TODO/appropriate error handling" — the two places that say "reuse the existing helper/fixture" (Task 7 env helper, Task 10 `make_ready_local_model`) point at concrete existing test helpers the implementer must look up in the named neighboring files; if none fits, write the ~10-line helper shown in Task 7.

**Type consistency:** `Options{Path,SkipReadyGate,Restart}`, `Outcome{ID,Action,Err}`, `Result{Outcomes,Changed,Warnings}`, `Apply(cfg, add, remove, o)`, `Check(cfg, ids, skipReady)`, `Sync(cfg, running, o)`, `ModelFor`, `LocalModels`, `Providers`, `WaitReady` are used identically in Tasks 3–5; `litellmFlags{JSON,DryRun,SkipReadyGate}` in Tasks 4/8; Python `BridgeResult/BridgeOutcome/LitellmStatus` in Tasks 9–11.

**Known risks called out for the executor**
1. First wt write reformats `config.yaml` whitespace (documented; comments preserved).
2. Task 10 Step 1 (registry/ready flags must be on disk before wt is called) can force a small reorder in `queue.py`/`local_control.py`; that is the only behavior change allowed there.
3. `omlx-6bit` has no entry in the policy table (ported verbatim from Python), so `wt start` of an `omlx-6bit` model warns "no LiteLLM mapping" exactly as `modelman start` errors today. Adding `omlx-6bit` is a one-line follow-up, deliberately out of scope.
