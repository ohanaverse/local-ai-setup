# Native Provider Alignment — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Align every agent's native provider with the agent's own name (rename `google` → `agy`, drop the `opencode` native provider), add validation that every agent has at least one provider, migrate existing configs to the new scheme, and remove the now-dead `opencode` native-driver branch.

**Architecture:** All logic lives in `internal/config/` (a new `migrateConfigSchema` function applied in `Load()`, plus updates to the legacy `Migrate()` seed logic and `validate()`) and `internal/agents/opencode.go` (drop the native branch). One doc file (`docs/wt-agents/agy-wt.md`) and one test file (`internal/agents/agents_test.go`) need targeted edits; `internal/config/migrate_test.go` needs broader updates plus new tests.

**Tech Stack:** Go 1.26, `BurntSushi/toml`, stdlib `os`/`fmt`/`slices`

## Conventions

- This codebase follows a `//` top-of-function comment convention: every test and exported function explains **what** it tests/does and **why** that matters. Keep that convention.
- Migration steps must be **idempotent** — running them twice on a stable config must be a no-op the second time.
- Commit messages: short, imperative, prefixed with the scope (e.g. `config: rename google provider to agy in legacy migrate`).
- Run `go test ./...` and `go vet ./...` after every step.

---

## Task 1: Add `migrateConfigSchema` (TDD: failing test first)

**Files:**
- Modify: `internal/config/migrate.go` (add `migrateConfigSchema`, `noNativeAgents`, `hasModel`)
- Modify: `internal/config/migrate_test.go` (add `TestMigrateConfigSchema` with three sub-tests)

### Step 1.1: Write the failing test

Add the following test to `internal/config/migrate_test.go`. It references `migrateConfigSchema` and helpers `countProviders`/`countModels`/`hasModel` that do not yet exist — the test will fail to compile, which is the expected starting point.

```go
// TestMigrateConfigSchema covers the three idempotent fixups applied to an
// already-decoded config (rename google→agy, ensure agy provider/model/agent,
// remove opencode native). A user with an older config.toml must end up with
// the new shape after a single Load(), and a second Load() must not touch
// the file. Failing this test means users with old configs would silently
// keep references to the dropped google/opencode-native entities.
func TestMigrateConfigSchema(t *testing.T) {
	tmp := t.TempDir()
	cfgDir := filepath.Join(tmp, "agent-wt")
	os.MkdirAll(cfgDir, 0755)
	t.Setenv("XDG_CONFIG_HOME", tmp)

	writeCfg := func(t *testing.T, body string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(cfgDir, "config.toml"), []byte(body), 0644); err != nil {
			t.Fatal(err)
		}
	}

	t.Run("rename google to agy", func(t *testing.T) {
		writeCfg(t, `
default_tag = "code"

[[providers]]
id = "google"
name = "Google"
location = "cloud"
[providers.auth]
type = "native"

[[models]]
id = "google/native"
family = "google"
provider_id = "google"
model_name = "native"
location = "cloud"
tags = ["code", "design"]

[[agents]]
name = "agy"
supported_providers = ["google"]
default_provider = "google"
`)
		cfg, err := Load()
		if err != nil {
			t.Fatalf("Load: %v", err)
		}

		// Provider renamed.
		var agy *Provider
		for i := range cfg.Providers {
			if cfg.Providers[i].ID == "agy" {
				agy = &cfg.Providers[i]
				break
			}
		}
		if agy == nil {
			t.Fatalf("agy provider missing after fixup: %+v", cfg.Providers)
		}
		if agy.Name != "Antigravity" {
			t.Errorf("agy provider Name = %q, want %q", agy.Name, "Antigravity")
		}
		if got := countProviders(cfg.Providers, "google"); got != 0 {
			t.Errorf("google provider still present: %d", got)
		}

		// Model renamed.
		if got := countModels(cfg.Models, "agy/native"); got != 1 {
			t.Errorf("agy/native models = %d, want 1", got)
		}
		if got := countModels(cfg.Models, "google/native"); got != 0 {
			t.Errorf("google/native models = %d, want 0", got)
		}

		// Agent rewired.
		a, err := cfg.AgentByName("agy")
		if err != nil {
			t.Fatalf("agy agent missing: %v", err)
		}
		if !slices.Equal(a.SupportedProviders, []string{"agy"}) || a.DefaultProvider != "agy" {
			t.Errorf("agy agent = %+v, want supported=[agy] default=agy", a)
		}
	})

	t.Run("strip opencode native", func(t *testing.T) {
		writeCfg(t, `
default_tag = "code"

[[providers]]
id = "ollama"
name = "Ollama"
[providers.auth]
type = "none"

[[providers]]
id = "opencode"
name = "OpenCode"
location = "cloud"
[providers.auth]
type = "native"

[[models]]
id = "opencode/native"
family = "opencode"
provider_id = "opencode"
model_name = "native"
location = "cloud"
tags = ["code"]

[[models]]
id = "ollama/deepseek-v4-pro:cloud"
family = "deepseek-v4-pro"
provider_id = "ollama"
model_name = "deepseek-v4-pro:cloud"
location = "cloud"
tags = ["code"]

[[agents]]
name = "opencode"
supported_providers = ["opencode"]
default_provider = "opencode"
`)
		cfg, err := Load()
		if err != nil {
			t.Fatalf("Load: %v", err)
		}

		if got := countProviders(cfg.Providers, "opencode"); got != 0 {
			t.Errorf("opencode provider still present: %d", got)
		}
		if got := countModels(cfg.Models, "opencode/native"); got != 0 {
			t.Errorf("opencode/native model still present: %d", got)
		}
		a, err := cfg.AgentByName("opencode")
		if err != nil {
			t.Fatalf("opencode agent missing: %v", err)
		}
		if !slices.Equal(a.SupportedProviders, []string{"ollama"}) || a.DefaultProvider != "ollama" {
			t.Errorf("opencode agent = %+v, want supported=[ollama] default=ollama", a)
		}

		// Ollama model survives.
		if got := countModels(cfg.Models, "ollama/deepseek-v4-pro:cloud"); got != 1 {
			t.Errorf("ollama model removed: %d", got)
		}
	})

	t.Run("idempotent", func(t *testing.T) {
		writeCfg(t, `
default_tag = "code"

[[providers]]
id = "ollama"
name = "Ollama"
[providers.auth]
type = "none"

[[agents]]
name = "claude"
supported_providers = ["claude", "ollama"]
default_provider = "claude"
`)
		// First Load applies any fixups (no-op for this clean config).
		if _, err := Load(); err != nil {
			t.Fatalf("first Load: %v", err)
		}
		firstData, err := os.ReadFile(filepath.Join(cfgDir, "config.toml"))
		if err != nil {
			t.Fatal(err)
		}
		// Second Load must not rewrite the file. With no fixups applied, the
		// file should be byte-identical.
		if _, err := Load(); err != nil {
			t.Fatalf("second Load: %v", err)
		}
		secondData, err := os.ReadFile(filepath.Join(cfgDir, "config.toml"))
		if err != nil {
			t.Fatal(err)
		}
		if string(firstData) != string(secondData) {
			t.Errorf("config.toml rewritten on second Load:\nbefore: %s\nafter:  %s", firstData, secondData)
		}
	})
}

// countProviders returns the number of providers whose ID matches id.
func countProviders(providers []Provider, id string) int {
	n := 0
	for _, p := range providers {
		if p.ID == id {
			n++
		}
	}
	return n
}

// countModels returns the number of models whose ID matches id.
func countModels(models []Model, id string) int {
	n := 0
	for _, m := range models {
		if m.ID == id {
			n++
		}
	}
	return n
}
```

Add `"slices"` to the import list of `internal/config/migrate_test.go` (it is NOT yet imported — verify and add `"slices"` between `"path/filepath"` and `"testing"`).

### Step 1.2: Run the test — verify it fails

```bash
go test ./internal/config -run TestMigrateConfigSchema -v
```

Expected: compile error (`migrateConfigSchema` undefined, or `countProviders`/`countModels`/`hasModel` undefined if you placed the helpers in `migrate.go` rather than the test file). Good — confirm failure before writing code.

### Step 1.3: Implement `migrateConfigSchema`

Add to `internal/config/migrate.go`:

```go
// noNativeAgents lists agents that, after this change, have no native
// provider: they only support ollama. The migration both skips creating a
// native provider for these agents (in case a legacy models.conf contains
// `native:opencode`) and ensures each one has an agent entry pinned to
// ollama only.
var noNativeAgents = map[string]bool{
	"pi":       true,
	"opencode": true,
}

// migrateConfigSchema applies idempotent fixups to an already-decoded cfg:
//   1. Rename the legacy "google" provider/model/agent references to "agy".
//   2. Ensure an agy provider, agy/native model, and agy agent exist.
//   3. Remove the opencode native provider/model and rewire the opencode
//      agent to ollama only.
//
// Each fixup is self-extinguishing — once applied, the old pattern no longer
// exists in cfg, so subsequent calls are no-ops. The boolean return is true
// iff any fixup actually changed cfg.
func migrateConfigSchema(cfg *Config) (bool, error) {
	changed := false

	// ── Fixup 1: rename "google" → "agy" everywhere ──────────────────
	for i := range cfg.Providers {
		if cfg.Providers[i].ID == "google" {
			cfg.Providers[i].ID = "agy"
			cfg.Providers[i].Name = "Antigravity"
			changed = true
		}
	}
	for i := range cfg.Models {
		if cfg.Models[i].ProviderID == "google" {
			cfg.Models[i].ProviderID = "agy"
			changed = true
		}
		if cfg.Models[i].ID == "google/native" {
			cfg.Models[i].ID = "agy/native"
			cfg.Models[i].Family = "agy"
			changed = true
		}
	}
	for i := range cfg.Agents {
		a := &cfg.Agents[i]
		rewired := false
		for j, p := range a.SupportedProviders {
			if p == "google" {
				a.SupportedProviders[j] = "agy"
				rewired = true
			}
		}
		if a.DefaultProvider == "google" {
			a.DefaultProvider = "agy"
			rewired = true
		}
		if rewired {
			changed = true
		}
	}

	// ── Fixup 2: ensure agy provider/model/agent exist ───────────────
	if cfg.ProviderByID("agy") == nil {
		cfg.Providers = append(cfg.Providers, Provider{
			ID:       "agy",
			Name:     "Antigravity",
			Location: LocationCloud,
			Auth:     AuthConfig{Type: "native"},
		})
		changed = true
	}
	if !hasModel(cfg.Models, "agy/native") {
		cfg.Models = append(cfg.Models, Model{
			ID:         "agy/native",
			Family:     "agy",
			ProviderID: "agy",
			ModelName:  "native",
			Location:   LocationCloud,
			Tags:       []string{"code", "design"},
		})
		changed = true
	}
	agyAgentFound := false
	for i := range cfg.Agents {
		if cfg.Agents[i].Name == "agy" {
			agyAgentFound = true
			if !slices.Equal(cfg.Agents[i].SupportedProviders, []string{"agy"}) ||
				cfg.Agents[i].DefaultProvider != "agy" {
				cfg.Agents[i].SupportedProviders = []string{"agy"}
				cfg.Agents[i].DefaultProvider = "agy"
				changed = true
			}
			break
		}
	}
	if !agyAgentFound {
		cfg.Agents = append(cfg.Agents, Agent{
			Name:               "agy",
			SupportedProviders: []string{"agy"},
			DefaultProvider:    "agy",
		})
		changed = true
	}

	// ── Fixup 3: remove opencode native provider/model ───────────────
	newProviders := cfg.Providers[:0]
	opencodeRemoved := false
	for _, p := range cfg.Providers {
		if p.ID == "opencode" {
			opencodeRemoved = true
			continue
		}
		newProviders = append(newProviders, p)
	}
	cfg.Providers = newProviders

	newModels := cfg.Models[:0]
	opencodeModelRemoved := false
	for _, m := range cfg.Models {
		if m.ProviderID == "opencode" {
			opencodeModelRemoved = true
			continue
		}
		newModels = append(newModels, m)
	}
	cfg.Models = newModels

	for i := range cfg.Agents {
		if cfg.Agents[i].Name != "opencode" {
			continue
		}
		if !slices.Equal(cfg.Agents[i].SupportedProviders, []string{"ollama"}) ||
			cfg.Agents[i].DefaultProvider != "ollama" {
			cfg.Agents[i].SupportedProviders = []string{"ollama"}
			cfg.Agents[i].DefaultProvider = "ollama"
			changed = true
		}
	}

	if opencodeRemoved || opencodeModelRemoved {
		changed = true
	}

	return changed, nil
}

// hasModel reports whether a model with the given id exists in models.
func hasModel(models []Model, id string) bool {
	for _, m := range models {
		if m.ID == id {
			return true
		}
	}
	return false
}
```

Add `"slices"` to the imports of `internal/config/migrate.go` (currently imports `fmt`, `os`, `path/filepath`, `regexp`, `strings` — confirm).

### Step 1.4: Verify the test compiles but does not yet pass

`migrateConfigSchema` is not yet called from `Load()`, so the rename/fixup tests will fail. Confirm the test now compiles (no missing-function errors), then move to Task 2.

---

## Task 2: Wire `migrateConfigSchema` into `Load()`

**Files:**
- Modify: `internal/config/config.go` (`Load()`)

### Step 2.1: Call `migrateConfigSchema` after decode

In `internal/config/config.go`, change the end of `Load()` to call the new function and persist + log if it changed:

```go
func Load() (*Config, error) {
	if _, err := Migrate(); err != nil {
		return nil, fmt.Errorf("migration: %w", err)
	}

	cfg := &Config{DefaultTag: "code"}
	data, err := os.ReadFile(Path())
	if os.IsNotExist(err) {
		return cfg, nil
	}
	if err != nil {
		return nil, err
	}
	if _, err := toml.Decode(string(data), cfg); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
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
	return cfg, nil
}
```

`fmt` and `os` are already imported in `config.go` (verify).

### Step 2.2: Run the tests

```bash
go test ./internal/config -v
go test ./...
go vet ./...
```

Expected: `TestMigrateConfigSchema` passes, all other config tests still pass.

### Step 2.3: Commit

```bash
git add internal/config/migrate.go internal/config/migrate_test.go internal/config/config.go
git commit -m "config: add migrateConfigSchema, wire into Load()"
```

---

## Task 3: Add validation for at least one provider per agent

**Files:**
- Modify: `internal/config/config.go` (`validate()`)
- Modify: `internal/config/config_test.go` (or create it) — add a validation test

### Step 3.1: Add the check

In `validate()`, inside the agent loop, add the check immediately after the `agentNames[a.Name] = true` line:

```go
if len(a.SupportedProviders) == 0 {
    errs = append(errs, fmt.Errorf("agent %q: must have at least one supported provider", a.Name))
}
```

Placed after `agentNames[a.Name] = true` so duplicate-name errors still surface first.

### Step 3.2: Add a test

Find the existing validation test file (search for `TestValidate*` — they live in `internal/config/config_test.go` or similar). Add a new test there:

```go
// An agent with no supported_providers is unusable: the launcher cannot pick
// a model for it. Validate must reject this so the user sees a clear error
// instead of a runtime "no eligible models" message during launch.
func TestValidate_AgentRequiresProvider(t *testing.T) {
	cfg := &Config{
		DefaultTag: "code",
		Providers:  []Provider{{ID: "ollama", Name: "Ollama", Auth: AuthConfig{Type: "none"}}},
		Agents:     []Agent{{Name: "lonely"}},
	}
	err := cfg.Validate()
	if err == nil {
		t.Fatal("expected validation error for empty supported_providers")
	}
	if !strings.Contains(err.Error(), "must have at least one supported provider") {
		t.Errorf("error = %q, want it to mention 'supported provider'", err)
	}
}
```

Add `"strings"` to the imports if not already present.

### Step 3.3: Run

```bash
go test ./internal/config -run TestValidate_AgentRequiresProvider -v
go test ./...
go vet ./...
```

Expected: passes; no other validation tests regress.

### Step 3.4: Commit

```bash
git add internal/config/config.go internal/config/config_test.go
git commit -m "config: require agents to have at least one supported provider"
```

---

## Task 4: Update legacy `Migrate()` seed logic (agy → agy, noNativeAgents)

**Files:**
- Modify: `internal/config/migrate.go` (replace the `pi`/`google`/`agy` blocks with the new scheme)

### Step 4.1: Update the native-processing loop

In `Migrate()`, in the `case "CODE_MODELS", "DESIGN_MODELS"` branch, change the loop that processes `natives` to skip `noNativeAgents`:

```go
for _, n := range natives {
    if nativeSeen[n] {
        continue
    }
    if noNativeAgents[n] {
        // Skip native provider/model creation for agents that don't have
        // a native provider after the schema alignment. We still add the
        // agent entry below, pinned to ollama.
        continue
    }
    nativeSeen[n] = true
    cfg.Providers = append(cfg.Providers, nativeProvider(n))
    cfg.Agents = append(cfg.Agents, nativeAgent(n))
}
```

### Step 4.2: Replace the pi/google/agy special-case blocks with the unified noNativeAgents loop

Remove the existing `pi` special-case block (the one that adds the `pi` agent if missing) and the `google`/`agy` blocks. Replace them with:

```go
// Ensure an agent entry exists for each noNativeAgent, pinned to ollama.
// For "pi", this is the only entry (it never appeared as `native:pi`).
// For "opencode", this corrects configs that may have seeded opencode via
// the native loop before this fix; after the early continue above, opencode
// will never get a native provider here, so this loop just creates a
// missing entry or rewires an existing one to ollama-only.
for name := range noNativeAgents {
    found := false
    for i := range cfg.Agents {
        if cfg.Agents[i].Name != name {
            continue
        }
        found = true
        // Pin to ollama only — strips any native references left over from
        // older migration runs.
        cfg.Agents[i].SupportedProviders = []string{"ollama"}
        cfg.Agents[i].DefaultProvider = "ollama"
        break
    }
    if !found {
        cfg.Agents = append(cfg.Agents, Agent{
            Name:               name,
            SupportedProviders: []string{"ollama"},
            DefaultProvider:    "ollama",
        })
    }
}

// Seed agy: provider + model + agent. Use the same naming scheme as the
// schema fixup so a fresh install ends up with the same shape that
// migrateConfigSchema would produce on an upgraded install.
if cfg.ProviderByID("agy") == nil {
    cfg.Providers = append(cfg.Providers, Provider{
        ID:       "agy",
        Name:     "Antigravity",
        Location: LocationCloud,
        Auth:     AuthConfig{Type: "native"},
    })
}
if !hasModel(cfg.Models, "agy/native") {
    cfg.Models = append(cfg.Models, Model{
        ID:         "agy/native",
        Family:     "agy",
        ProviderID: "agy",
        ModelName:  "native",
        Location:   LocationCloud,
        Tags:       []string{"code", "design"},
    })
}
agyFound := false
for i := range cfg.Agents {
    if cfg.Agents[i].Name == "agy" {
        agyFound = true
        cfg.Agents[i].SupportedProviders = []string{"agy"}
        cfg.Agents[i].DefaultProvider = "agy"
        break
    }
}
if !agyFound {
    cfg.Agents = append(cfg.Agents, Agent{
        Name:               "agy",
        SupportedProviders: []string{"agy"},
        DefaultProvider:    "agy",
    })
}
```

### Step 4.3: Confirm intermediate state

```bash
go test ./internal/config -run TestMigrate -v
```

Expected: `TestMigrate_EndToEnd` and `TestMigrate_AgyGoogleSeedingIsIdempotent` may now fail because they expect the old `google` provider / `google/native` model. That's expected — Task 5 updates them.

---

## Task 5: Update migrate tests for the new shape

**Files:**
- Modify: `internal/config/migrate_test.go`

### Step 5.1: Rename `TestMigrate_AgyGoogleSeedingIsIdempotent` → `TestMigrate_AgySeedingIsIdempotent`

Update its body to assert against `agy` instead of `google`:

```go
// Migrate must keep agy restricted to its own agy provider and avoid
// duplicating agy provider/model when a legacy config includes `native:agy`.
func TestMigrate_AgySeedingIsIdempotent(t *testing.T) {
	tmp := t.TempDir()
	cfgDir := filepath.Join(tmp, "agent-wt")
	os.MkdirAll(cfgDir, 0755)

	legacy := `CODE_MODELS=(
	  "native:agy"
	)
	`
	os.WriteFile(filepath.Join(cfgDir, "models.conf"), []byte(legacy), 0644)
	t.Setenv("XDG_CONFIG_HOME", tmp)

	migrated, err := Migrate()
	if err != nil {
		t.Fatalf("Migrate() error = %v", err)
	}
	if !migrated {
		t.Fatal("expected migration to run")
	}

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}

	agyProviders := 0
	for _, p := range cfg.Providers {
		if p.ID == "agy" {
			agyProviders++
		}
	}
	if agyProviders != 1 {
		t.Fatalf("expected exactly 1 agy provider, got %d", agyProviders)
	}

	agyNativeModels := 0
	for _, m := range cfg.Models {
		if m.ID == "agy/native" {
			agyNativeModels++
			if !m.HasTag("code") || !m.HasTag("design") {
				t.Fatalf("agy/native tags = %v, want both code and design", m.Tags)
			}
		}
	}
	if agyNativeModels != 1 {
		t.Fatalf("expected exactly 1 agy/native model, got %d", agyNativeModels)
	}

	for _, a := range cfg.Agents {
		if a.Name == "agy" {
			if len(a.SupportedProviders) != 1 || a.SupportedProviders[0] != "agy" || a.DefaultProvider != "agy" {
				t.Fatalf("agy agent not restricted to agy provider: %+v", a)
			}
			return
		}
	}
	t.Fatal("expected agy agent in migrated config")
}
```

### Step 5.2: Update `TestMigrate_EndToEnd`

The test feeds a legacy config containing `native:copilot`. After the changes:
- **Providers**: ollama + copilot + agy (still 3, but the index of `copilot` may shift from `1` to `1` or `2` depending on insertion order).
- **Models**: copilot/native + deepseek + kimi + agy/native (4 — same count, but `agy/native` replaces `google/native`).
- **Agents**: copilot + pi + opencode + agy (4 — was 3; the `opencode` agent is now seeded by the `noNativeAgents` loop).

Replace the providers block with ID-based lookups (robust against insertion order):

```go
// Providers: ollama + copilot + agy (3 total).
if len(cfg.Providers) != 3 {
    t.Fatalf("expected 3 providers, got %d", len(cfg.Providers))
}
providerByID := map[string]Provider{}
for _, p := range cfg.Providers {
    providerByID[p.ID] = p
}
ollama, ok := providerByID["ollama"]
if !ok || ollama.Auth.BaseURL != "http://localhost:9999" {
    t.Errorf("ollama provider missing or wrong base URL: %+v", ollama)
}
copilot, ok := providerByID["copilot"]
if !ok || copilot.Auth.Type != "native" {
    t.Errorf("copilot provider wrong: %+v", copilot)
}
agy, ok := providerByID["agy"]
if !ok || agy.Name != "Antigravity" {
    t.Errorf("agy provider wrong: %+v", agy)
}
```

Replace the agents block:

```go
// Agents: copilot (from nativeSeen), pi + opencode (noNativeAgents loop), agy.
// 4 total — opencode is now seeded by the noNativeAgents loop.
if len(cfg.Agents) != 4 {
    t.Fatalf("expected 4 agents, got %d: %v", len(cfg.Agents), cfg.Agents)
}
expectedAgents := []string{"copilot", "pi", "opencode", "agy"}
for i, want := range expectedAgents {
    if cfg.Agents[i].Name != want {
        t.Errorf("cfg.Agents[%d].Name = %q, want %q", i, cfg.Agents[i].Name, want)
    }
}
```

The models count assertion (4) and the commented-out-model check still hold. The copilot-model assertion still holds. No other model-related changes needed.

### Step 5.3: Run

```bash
go test ./internal/config -run TestMigrate -v
go test ./...
go vet ./...
```

Expected: all `TestMigrate*` tests pass; all package tests pass.

### Step 5.4: Commit

```bash
git add internal/config/migrate.go internal/config/migrate_test.go
git commit -m "config: switch legacy Migrate seed to agy provider + noNativeAgents"
```

---

## Task 6: Remove the `opencode` native branch from `opencodeDriver`

**Files:**
- Modify: `internal/agents/opencode.go`
- Modify: `internal/agents/agents_test.go`

### Step 6.1: Replace the driver body

Replace the `Build` method in `internal/agents/opencode.go`:

```go
package agents

import (
	"fmt"

	"github.com/ohanaverse/agent-worktree/internal/config"
)

func init() { register("opencode", func() Driver { return opencodeDriver{} }) }

type opencodeDriver struct{}

func (opencodeDriver) YoloFlag() string { return "--dangerously-skip-permissions" }

// OpenCode is ollama-only after the native-provider alignment. The
// OPENCODE_CONFIG_CONTENT env var routes through the ollama gateway with
// the provider/model form ("ollama/<ModelName>"). The bare provider-side
// name comes from m.ModelName, not m.ID — m.ID already carries the
// "ollama/" registry prefix, so using it would produce
// "ollama/ollama/<model>" (a double prefix that the gateway rejects).
func (opencodeDriver) Build(m config.Model, yolo bool) LaunchCmd {
	lc := LaunchCmd{Bin: "opencode"}
	if yolo {
		lc.Args = append(lc.Args, opencodeDriver{}.YoloFlag())
	}
	lc.Env = append(lc.Env,
		"OPENCODE_CONFIG_CONTENT="+fmt.Sprintf(
			`{"model":"ollama/%s","provider":{"ollama":{"options":{"baseURL":"%s/v1","apiKey":""}}}}`,
			m.ModelName, config.OllamaBaseURL,
		),
	)
	return lc
}
```

### Step 6.2: Update `TestOpenCode` in `agents_test.go`

Remove the native-model assertions from `TestOpenCode`. The new body:

```go
// OpenCode receives its model configuration via a single
// OPENCODE_CONFIG_CONTENT env var containing inline JSON. After the
// native-provider alignment, opencode is ollama-only — there is no native
// branch to test.
func TestOpenCode(t *testing.T) {
	d := ByName("opencode")
	if d == nil {
		t.Fatal("opencode driver not registered")
	}
	lc := d.Build(ollamaCloudModel("deepseek-v4-pro:cloud"), false)
	if len(lc.Args) != 0 {
		t.Errorf("opencode should not pass --model, got args %v", lc.Args)
	}
	if len(lc.Env) != 1 || !strings.Contains(lc.Env[0], "OPENCODE_CONFIG_CONTENT=") {
		t.Fatalf("env = %v, want OPENCODE_CONFIG_CONTENT", lc.Env)
	}
	if !strings.Contains(lc.Env[0], `"model":"ollama/deepseek-v4-pro:cloud"`) {
		t.Errorf("config missing model: %s", lc.Env[0])
	}
}
```

### Step 6.3: Remove `TestOpenCodeNativeProviderNamed`

This test exercises a code path that no longer exists. Delete the function entirely (it is the test block immediately above `TestBuildLaunchCmdNativeSkipsResume`).

### Step 6.4: Run

```bash
go test ./internal/agents -v
go test ./...
go vet ./...
```

Expected: all agents tests pass, including `TestOpenCodeOllamaPrefix` (still valid for the ollama path).

### Step 6.5: Commit

```bash
git add internal/agents/opencode.go internal/agents/agents_test.go
git commit -m "opencode: drop native-driver branch (ollama-only after alignment)"
```

---

## Task 7: Update `docs/wt-agents/agy-wt.md`

**Files:**
- Modify: `docs/wt-agents/agy-wt.md`

### Step 7.1: Update the model selection section

Replace the existing "Model selection" section with:

```markdown
## Model selection

`agy-wt` does **not** pass a `--model` flag through to `agy`. Model selection happens inside `agy`'s TUI via `/model`.

`agy` is restricted to the `agy` provider. The launcher only considers `agy` models when building the eligible list, and it skips the model picker when exactly one eligible model exists.
```

### Step 7.2: Verify

```bash
make check
```

Expected: passes (this doc is markdown, no shell impact).

### Step 7.3: Commit

```bash
git add docs/wt-agents/agy-wt.md
git commit -m "docs(agy-wt): rename google provider reference to agy"
```

---

## Task 8: Update `docs/wt-agents/opencode-wt.md`

**Files:**
- Modify: `docs/wt-agents/opencode-wt.md`

### Step 8.1: Update the model selection section

Replace the existing bullet list in the "Model selection" section with:

```markdown
## Model selection

OpenCode selects models via `--model provider/model` (e.g., `--model anthropic/claude-sonnet-4-5`). `opencode-wt` handles ollama models by generating inline JSON via the `OPENCODE_CONFIG_CONTENT` environment variable:

```json
{"model":"ollama/<model>","provider":{"ollama":{"options":{"baseURL":"http://localhost:11434/v1","apiKey":""}}}}
```

OpenCode is the one agent whose CLI uniquely requires the `provider/model` form, so the launcher constructs the literal `ollama/` prefix from the **bare** provider-specific name (`config.Model.ModelName`), not from `config.Model.ID`. Using `m.ID` here would produce `ollama/ollama/<model>` (a double prefix) because the registry already prefixes IDs with the provider id. This is the symmetric trap to the one in `claude-wt`/`codex-wt`/`copilot-wt`, where the launcher must NOT add a prefix.

The base URL is the `config.OllamaBaseURL` constant (`http://localhost:11434`) with a `/v1` suffix. `OPENCODE_CONFIG_CONTENT` is OpenCode's highest-precedence layer and overrides any conflicting key in `~/.config/opencode/opencode.json` (e.g. `model`, `provider.ollama.options.baseURL`).
```

(Removed the `opencode/native` bullet since that branch no longer exists; kept everything else.)

### Step 8.2: Verify and commit

```bash
make check
git add docs/wt-agents/opencode-wt.md
git commit -m "docs(opencode-wt): remove opencode/native case (ollama-only)"
```

---

## Task 9: Final verification

**Files:** none (read-only verification)

### Step 9.1: Full test suite + vet

```bash
go test ./...
go vet ./...
```

Expected: all tests pass, no vet errors.

### Step 9.2: Build

```bash
make build
```

Expected: `bin/wt` built; on macOS the ad-hoc signature is re-sealed automatically.

### Step 9.3: Smoke-test the migration

In a temp `XDG_CONFIG_HOME`, hand-craft a `config.toml` with a `google` provider and `opencode/native` model, then run:

```bash
XDG_CONFIG_HOME=/tmp/wt-smoke go run ./cmd/wt models
```

Expected: stderr shows `wt: migrated config to native-provider alignment ...`; the listed providers include `agy` (not `google`) and no `opencode` provider; the listed models include `agy/native` (not `google/native`) and no `opencode/native`.

After the first run, run the same command again:

```bash
XDG_CONFIG_HOME=/tmp/wt-smoke go run ./cmd/wt models
```

Expected: no migration line on stderr; output identical to the first run.

### Step 9.4: Clean up the smoke dir

```bash
rm -rf /tmp/wt-smoke
```

---

## Done

All tasks committed. The config schema now enforces "provider id matches agent id" for the native cases (`agy`, `claude`, `copilot`, `codex`), `opencode` is purely ollama-routed, validation rejects agents with no providers, and legacy configs auto-migrate on next `Load()`.
