# `nyt-litellm` provider for pi Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Let `pi`, launched through `wt`, route through NYT's enterprise LiteLLM gateway (`https://llm-gateway.nyt.net`) using a Vault-backed key that wt resolves itself at launch time — replacing the hand-maintained `~/.pi/agent/activate-litellm.sh` wrapper and its hand-written `models.json` block.

**Architecture:** Add a new `exec:<path>` form to wt's `internal/config.ResolveSecret` so a `secret_ref` can run an external command (the existing enterprise credential helper) and return its stdout as the key, erroring loudly on failure and memoizing the result for the life of one `wt` process. Wire this up purely through data (a new `nyt-litellm` provider/model row in `registry.toml` and a `pi` agent entry in `config.toml`) — the existing generic `syncDirectProviders` logic in `internal/agents/pi_models.go` already knows how to write a pi `models.json` provider block for any registry provider with a `base_url`, so it needs no new code, only its error-swallowing path fixed to not eat a real `exec:` failure.

**Tech Stack:** Go 1.26 (wt module), `os/exec`, `sync.Map` for memoization, TOML config files, pi's JSON `models.json` catalog.

**Spec:** `docs/superpowers/specs/2026-09-23-nyt-litellm-provider-design.md`

## Global Constraints

- Only the new `exec:` secret_ref form may return an error from `ResolveSecret`; the existing `os.environ/NAME` and bare-`NAME` forms must keep returning `""` on a miss, never an error (per spec Section 1).
- `exec:` resolution must be memoized per raw `ref` string for the lifetime of one `wt` process — the Vault-backed helper must run at most once per launch even though both `ResolveRoute` and pi's `models.json` sync resolve the same ref (per spec Section 1).
- A `secret_ref` resolution failure must abort the launch before pi is exec'd, surfacing the helper script's own stderr text, not a generic exit-status message (per spec Section 6).
- No changes to wt's `[litellm]` proxy on/off toggle or `wt litellm` command family — `nyt-litellm` is a direct-mode provider only (per spec Non-goals).
- Scoped to `pi` only; the registry provider entry itself stays agent-agnostic (per spec Non-goals).

## Review Focus

- **A `secret_ref` with no `exec:`/`os.environ/` prefix and not matching the bare-env-name pattern** (e.g. a literal API key string, or a malformed value like `exec` with no colon) — must still resolve verbatim with no error, exactly like today. Covered in Task 1's table-driven test (the "literal, unchanged" case already exists; Task 1 must not regress it).
- **The `exec:` command exits non-zero with no stderr output at all** (e.g. `chmod`-blocked script, missing binary) — the wrapped error must not panic or print an empty/nil message; `*exec.ExitError` on some failures has empty `Stderr` when output wasn't captured. Covered in Task 1 with a case exec'ing a nonexistent path.
- **`syncDirectProviders` is called once per launch but through two different call sites in the same process** (`ResolveRoute` inside `agents.BuildLaunchCmd`, then `SyncModels`/`syncDirectProviders` right after, per `internal/agents/agents.go:324-328`) — a non-memoized implementation would shell out twice per launch. Covered in Task 1 with a counting fake command.
- **An agent config with `supported_providers = ["nyt-litellm"]` but no matching registry provider** (a typo, or registry not yet updated) — `Config.Validate()` must still catch this the same way it catches any other unknown-provider reference (existing `validate()` logic, not new code) — confirmed in Task 4 as a manual/existing-behavior check, not a regression.
- **`wantAPIKey == ""` skip in `syncDirectProviders` must not silently absorb an `exec:` error** — today any resolution failure (there wasn's one before) falls through to the empty-string skip-provider branch. After Task 2, an `exec:` error must propagate out of `syncModels`/`SyncModels` as a real error, not vanish as "provider skipped, zero models written." Covered explicitly in Task 2.

---

## Task 1: `exec:` form for `ResolveSecret`

**Files:**
- Modify: `wt/internal/config/config.go:254-267` (the `ResolveSecret` function and its doc comment)
- Modify: `wt/internal/config/config.go:190-193` (the one call site inside `ResolveRoute`)
- Test: `wt/internal/config/config_test.go` (extend `TestResolveSecretEnvAndLiteral`-adjacent tests)

**Interfaces:**
- Consumes: nothing new — `os/exec`, `strings`, `sync` (stdlib only).
- Produces: `func ResolveSecret(ref string) (string, error)` — **signature change** from today's `func ResolveSecret(ref string) string`. Every caller in the codebase (there are exactly two: `config.go:192` inside `ResolveRoute`, and `pi_models.go:298` inside `syncDirectProviders`) must be updated in this task and Task 2 respectively. This task updates the `config.go` call site; Task 2 updates the `pi_models.go` one.

- [ ] **Step 1: Write the failing tests**

Replace the existing `TestResolveSecretEnvAndLiteral` in `wt/internal/config/config_test.go` (lines 32-43) with a table-driven version covering all forms, including the new `exec:` one, with the new two-return signature:

```go
// TestResolveSecret pins every secret_ref form: os.environ/NAME and bare
// NAME env lookups (never error, empty on a miss), a literal value used
// verbatim, and the exec: form that runs a command and returns its trimmed
// stdout, erroring (with the command's own stderr surfaced) on a non-zero
// exit. Getting the exec: form wrong either silently produces an empty key
// (a confusing downstream 401) or panics on a command with no captured
// stderr.
func TestResolveSecret(t *testing.T) {
	t.Setenv("OPENROUTER_API_KEY", "sk-or-from-env")

	scriptDir := t.TempDir()
	okScript := filepath.Join(scriptDir, "ok.sh")
	if err := os.WriteFile(okScript, []byte("#!/bin/sh\necho '  sk-vault-key  '\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	failScript := filepath.Join(scriptDir, "fail.sh")
	if err := os.WriteFile(failScript, []byte("#!/bin/sh\necho 'permission denied' >&2\nexit 3\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name    string
		ref     string
		want    string
		wantErr string // substring expected in the error, "" if no error expected
	}{
		{name: "os.environ form", ref: "os.environ/OPENROUTER_API_KEY", want: "sk-or-from-env"},
		{name: "bare env-name form", ref: "OPENROUTER_API_KEY", want: "sk-or-from-env"},
		{name: "os.environ form missing var", ref: "os.environ/NO_SUCH_VAR", want: ""},
		{name: "bare env-name form missing var", ref: "NO_SUCH_VAR_XYZ", want: ""},
		{name: "literal form", ref: "sk-or-v1-literal", want: "sk-or-v1-literal"},
		{name: "exec form success, trims whitespace", ref: "exec:" + okScript, want: "sk-vault-key"},
		{name: "exec form failure surfaces stderr", ref: "exec:" + failScript, wantErr: "permission denied"},
		{name: "exec form nonexistent binary", ref: "exec:/no/such/binary-xyz", wantErr: "exec:"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ResolveSecret(tc.ref)
			if tc.wantErr != "" {
				if err == nil {
					t.Fatalf("ResolveSecret(%q): want error containing %q, got nil (result %q)", tc.ref, tc.wantErr, got)
				}
				if !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("ResolveSecret(%q) error = %q, want substring %q", tc.ref, err.Error(), tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("ResolveSecret(%q): unexpected error: %v", tc.ref, err)
			}
			if got != tc.want {
				t.Errorf("ResolveSecret(%q) = %q, want %q", tc.ref, got, tc.want)
			}
		})
	}
}

// TestResolveSecretExecMemoized pins that an exec: ref only actually runs
// the command once per process, no matter how many times it is resolved —
// ResolveRoute and pi's models.json sync both resolve the same ref on every
// pi launch, and the underlying command here is a Vault-backed credential
// helper that may trigger a slow network call or an interactive OIDC login
// prompt. A non-memoized implementation would pay that cost twice per
// launch.
func TestResolveSecretExecMemoized(t *testing.T) {
	dir := t.TempDir()
	counterFile := filepath.Join(dir, "count")
	script := filepath.Join(dir, "counted.sh")
	// Each invocation appends one byte to counterFile and echoes the key.
	body := "#!/bin/sh\necho -n x >> " + counterFile + "\necho sk-counted\n"
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	ref := "exec:" + script

	for i := 0; i < 3; i++ {
		got, err := ResolveSecret(ref)
		if err != nil {
			t.Fatalf("ResolveSecret call %d: %v", i, err)
		}
		if got != "sk-counted" {
			t.Errorf("call %d: got %q", i, got)
		}
	}

	data, err := os.ReadFile(counterFile)
	if err != nil {
		t.Fatal(err)
	}
	if len(data) != 1 {
		t.Errorf("command ran %d times across 3 resolutions of the same ref, want 1 (memoized)", len(data))
	}
}
```

Add `"path/filepath"` and `"strings"` to the test file's imports if not already present (check the existing import block at the top of `config_test.go` — `os` and `path/filepath` and `strings` are already imported per the earlier `TestPath` test, so likely no import changes needed).

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd wt && go test ./internal/config/... -run 'TestResolveSecret' -v`
Expected: compile error, since `ResolveSecret` still returns a single `string` and the test calls it expecting `(string, error)`.

- [ ] **Step 3: Implement the `exec:` form and signature change**

Replace `wt/internal/config/config.go:254-267`:

```go
// ResolveSecret resolves a secret_ref-style value. Three forms:
//   - "exec:<path> [args...]" runs the command (no shell — split on
//     whitespace) and returns its trimmed stdout; a non-zero exit is an
//     error whose text includes the command's own stderr. Memoized per raw
//     ref for the lifetime of the process, so a ref resolved from multiple
//     call sites in one launch (ResolveRoute and pi's models.json sync both
//     resolve the same provider's secret_ref) only runs the command once.
//   - "os.environ/NAME" or a bare "^[A-Z][A-Z0-9_]*$" name reads that env
//     var — unchanged: a missing var resolves to "", never an error.
//   - anything else is used verbatim, unchanged.
//
// Shared by direct-mode provider auth and [litellm].api_key (the latter
// never uses the exec: form today, but nothing prevents it).
var envRefName = regexp.MustCompile(`^[A-Z][A-Z0-9_]*$`)

var execSecretCache sync.Map // ref string -> execSecretResult

type execSecretResult struct {
	value string
	err   error
}

func ResolveSecret(ref string) (string, error) {
	if cmdline, ok := strings.CutPrefix(ref, "exec:"); ok {
		if cached, ok := execSecretCache.Load(ref); ok {
			r := cached.(execSecretResult)
			return r.value, r.err
		}
		parts := strings.Fields(cmdline)
		if len(parts) == 0 {
			err := fmt.Errorf("secret_ref %q: exec: form has no command", ref)
			execSecretCache.Store(ref, execSecretResult{"", err})
			return "", err
		}
		out, runErr := exec.Command(parts[0], parts[1:]...).Output()
		value := strings.TrimSpace(string(out))
		var err error
		if runErr != nil {
			if exitErr, ok := runErr.(*exec.ExitError); ok && len(exitErr.Stderr) > 0 {
				err = fmt.Errorf("secret_ref %q: %s", ref, strings.TrimSpace(string(exitErr.Stderr)))
			} else {
				err = fmt.Errorf("secret_ref %q: %w", ref, runErr)
			}
			value = ""
		}
		execSecretCache.Store(ref, execSecretResult{value, err})
		return value, err
	}
	if name, ok := strings.CutPrefix(ref, "os.environ/"); ok {
		return os.Getenv(name), nil
	}
	if envRefName.MatchString(ref) {
		return os.Getenv(ref), nil
	}
	return ref, nil
}
```

Add `"os/exec"` and `"sync"` to `config.go`'s import block (it already imports `os`, `regexp`, `strings`, `fmt`).

Note on `exec.Command(...).Output()` and `Stderr`: `Output()` only populates `ExitError.Stderr` when the caller hasn't already set `cmd.Stderr` — this is the default (zero-value) case here, so `Output()`'s documented behavior applies: on a non-zero exit it returns an `*exec.ExitError` with `Stderr` holding up to 32KB of the command's stderr. This matches the credential helper's own convention (it writes `Error: ...` lines to stderr via its `error()` function).

Now fix the one call site inside `ResolveRoute` (`config.go:190-193`):

```go
	apiKey := ""
	if provider.Auth.SecretRef != "" {
		var err error
		apiKey, err = ResolveSecret(provider.Auth.SecretRef)
		if err != nil {
			return Route{}, fmt.Errorf("resolving credentials for provider %q: %w", providerID, err)
		}
	}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd wt && go test ./internal/config/... -run 'TestResolveSecret' -v`
Expected: PASS (`TestResolveSecret` with all 8 subtests, `TestResolveSecretExecMemoized`).

- [ ] **Step 5: Run the full config package test suite to check for fallout**

Run: `cd wt && go test ./internal/config/... -v 2>&1 | tail -60`
Expected: all pre-existing tests still PASS (the `ResolveRoute` signature is unchanged — only its body changed — so no other test should need updates). If any test in `config_test.go` or elsewhere calls the old single-return `ResolveSecret(...)` directly, fix it to handle the new two-return form.

- [ ] **Step 6: Commit**

```bash
cd wt
git add internal/config/config.go internal/config/config_test.go
git commit -m "wt: add exec: secret_ref form for external credential helpers"
```

---

## Task 2: Propagate `exec:` errors through pi's `models.json` sync

**Files:**
- Modify: `wt/internal/agents/pi_models.go:279-436` (`syncDirectProviders` signature and its one `ResolveSecret` call at line 298; and its two callers)
- Modify: `wt/internal/agents/pi_models.go:128-165` (`syncModels`, the function that calls `syncDirectProviders`)
- Modify: `wt/internal/agents/pi.go:20-26` (`SyncModels` — return type unaffected, but confirm it already threads the error through, which it does)
- Test: `wt/internal/agents/pi_models_test.go` (new test)

**Interfaces:**
- Consumes: `config.ResolveSecret(ref string) (string, error)` from Task 1.
- Produces: `func syncDirectProviders(cfg *config.Config, f piModelsFile) (bool, error)` — **signature change** from today's `func syncDirectProviders(cfg *config.Config, f piModelsFile) bool`. `syncModels` (its only caller, `pi_models.go:154`) is updated in this task to handle the new return; `syncModels`'s own signature (`func syncModels(cfg *config.Config, path string) error`) is unchanged — it already returns an error.

- [ ] **Step 1: Write the failing test**

Add to `wt/internal/agents/pi_models_test.go` (place near `TestSyncModelsDirectPreservesCustomProvider`, using the same `writeFile`/`emptyPiModels` helpers already in this file):

```go
// A provider whose secret_ref is an exec: form that fails (e.g. the Vault
// credential helper isn't logged in) must abort the whole sync with that
// error, not silently skip the provider block the way a merely-unset
// os.environ/NAME secret_ref does. Before this fix, ResolveSecret had no
// error return at all, so a failing exec: helper and a genuinely-unset env
// var were indistinguishable — both produced "" and got skipped.
func TestSyncModelsDirectPropagatesExecSecretError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "models.json")
	writeFile(t, path, emptyPiModels)

	scriptDir := t.TempDir()
	failScript := filepath.Join(scriptDir, "fail.sh")
	if err := os.WriteFile(failScript, []byte("#!/bin/sh\necho 'no key found' >&2\nexit 2\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	cfg := &config.Config{
		Providers: []config.Provider{
			{ID: "nyt-litellm", Auth: config.AuthConfig{Type: "api_key", BaseURL: "https://llm-gateway.nyt.net", SecretRef: "exec:" + failScript}},
		},
		Models: []config.Model{
			{ID: "nyt-litellm/claude-sonnet-4-6", ModelName: "claude-sonnet-4-6", ProviderID: "nyt-litellm"},
		},
	}

	err := syncModels(cfg, path)
	if err == nil {
		t.Fatal("syncModels: want error from failing exec: secret_ref, got nil")
	}
	if !strings.Contains(err.Error(), "no key found") {
		t.Errorf("syncModels error = %q, want it to include the helper's stderr (\"no key found\")", err.Error())
	}
}
```

Add `"strings"` to this test file's imports if not already present (check the existing import block — it currently has `encoding/json`, `os`, `path/filepath`, `slices`, `testing`, plus the config import; `strings` is likely not yet imported here).

- [ ] **Step 2: Run test to verify it fails**

Run: `cd wt && go test ./internal/agents/... -run TestSyncModelsDirectPropagatesExecSecretError -v`
Expected: FAIL — today `syncModels` returns `nil` because `ResolveSecret` never errors and the empty-string branch just skips the provider block silently.

- [ ] **Step 3: Implement the fix**

In `wt/internal/agents/pi_models.go`, change the `syncDirectProviders` signature (line 279) and its `ResolveSecret` call (lines 295-306):

```go
func syncDirectProviders(cfg *config.Config, f piModelsFile) (bool, error) {
	byProvider := map[string][]config.Model{}
	for _, m := range cfg.Models {
		if m.Native || m.ModelName == "" {
			continue
		}
		byProvider[m.ProviderID] = append(byProvider[m.ProviderID], m)
	}

	mutated := false
	for providerID, models := range byProvider {
		provider := cfg.ProviderByID(providerID)
		if provider == nil || provider.Auth.BaseURL == "" {
			continue
		}

		wantBaseURL := config.BaseOrigin(provider.Auth.BaseURL) + "/v1"
		wantAPIKey := defaultPiOllamaAPIKey
		if provider.Auth.SecretRef != "" {
			var err error
			wantAPIKey, err = config.ResolveSecret(provider.Auth.SecretRef)
			if err != nil {
				return false, fmt.Errorf("pi models.json sync: provider %q: %w", providerID, err)
			}
		}
		// pi's models.json schema requires a non-empty apiKey (see
		// defaultPiOllamaAPIKey); a provider whose secret is simply unset
		// (an os.environ/NAME or bare-name ref that resolved to "" with no
		// error) cannot produce a schema-valid block, so skip it entirely
		// rather than write one and poison the whole catalog. An exec: form
		// that fails outright is handled above and never reaches here.
		if wantAPIKey == "" {
			continue
		}
```

(The rest of the function body — everything after this point, through the closing `return mutated` at line 436 — is unchanged in content, but its final line must change from `return mutated` to `return mutated, nil`.)

Add `"fmt"` to `pi_models.go`'s import block if not already present (it currently imports `encoding/json`, `os`, `path/filepath`, and the config package — check and add `fmt` if missing).

Now update the one call site in `syncModels` (`pi_models.go:149-155`):

```go
	mutated := false
	if cfg.IsLitellm() {
		mutated = syncLitellmProvider(cfg, f) || mutated
		mutated = revertOllamaProvider(cfg, f) || mutated
	} else {
		var err error
		var directMutated bool
		directMutated, err = syncDirectProviders(cfg, f)
		if err != nil {
			return err
		}
		mutated = directMutated || mutated
	}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `cd wt && go test ./internal/agents/... -run TestSyncModelsDirectPropagatesExecSecretError -v`
Expected: PASS.

- [ ] **Step 5: Run the full agents package test suite to check for fallout**

Run: `cd wt && go test ./internal/agents/... -v 2>&1 | tail -80`
Expected: all pre-existing tests still PASS, including `TestSyncModelsDirectPreservesCustomProvider` and `TestSyncModelsDirectResyncsStaleNonOllamaProvider` (neither uses `secret_ref`, so `ResolveSecret` returns `("", nil)` or a literal value with `nil` error for them — behavior unchanged).

- [ ] **Step 6: Commit**

```bash
cd wt
git add internal/agents/pi_models.go internal/agents/pi_models_test.go
git commit -m "wt: propagate exec: secret_ref failures out of pi models.json sync"
```

---

## Task 3: Full build/vet/test pass for wt

**Files:** none (verification only)

**Interfaces:**
- Consumes: everything from Tasks 1-2.
- Produces: a verified-green wt module, ready for the machine-local config changes in Task 4.

- [ ] **Step 1: Build**

Run: `cd wt && go build ./...`
Expected: no errors. This is the first point every other package that might transitively reference `ResolveSecret` or `syncDirectProviders` gets checked — per Task 1/2's own greps, there are no other call sites, but a clean build confirms it.

- [ ] **Step 2: Vet**

Run: `cd wt && go vet ./...`
Expected: no findings.

- [ ] **Step 3: Full test suite**

Run: `cd wt && go test ./...`
Expected: all packages PASS.

- [ ] **Step 4: Reinstall the local `wt` binary**

Run: `cd wt && make install`
Expected: succeeds (rebuilds and re-signs `~/.local/bin/wt`) — Task 5's manual verification launches the installed binary, not a stale one from a previous `make install`, per `wt/CLAUDE.md`'s "Verifying a branch's behavior against live data requires building it first" note.

- [ ] **Step 5: Commit (only if any fixups were needed in Steps 1-3; otherwise skip)**

```bash
cd wt
git add -A
git commit -m "wt: fix build/vet/test fallout from exec: secret_ref change"
```

If Steps 1-3 were already clean, there is nothing to commit here — proceed to Task 4.

---

## Task 4: Machine-local config — registry entry, agent entry, pi cleanup

**Files:**
- Modify: `~/.config/local-ai/registry.toml` (outside the repo — machine-local state, not tracked by this git worktree)
- Create: `~/.config/agent-wt/config.toml` (outside the repo — does not exist yet on this machine)
- Modify: `~/.pi/agent/models.json` (outside the repo — remove the stale hand-written `"anthropic"` block)
- Delete: `~/.pi/agent/activate-litellm.sh` (outside the repo)

**Interfaces:**
- Consumes: the `exec:` secret_ref form from Task 1, applied as data (no code needed).
- Produces: a working `nyt-litellm` provider that Task 5's manual launch verifies end-to-end.

- [ ] **Step 1: Back up the two config files being touched**

```bash
cp ~/.pi/agent/models.json ~/.pi/agent/models.json.bak-2026-09-23
cp ~/.pi/agent/activate-litellm.sh ~/.pi/agent/activate-litellm.sh.bak-2026-09-23
```

(`~/.config/local-ai/registry.toml` and `~/.config/agent-wt/config.toml` don't need backups — the former is currently empty scaffolding, `providers = []` / `models = []`, and the latter doesn't exist yet.)

- [ ] **Step 2: Write the registry entry**

Edit `~/.config/local-ai/registry.toml` to replace its current empty scaffolding:

```toml
providers = []
families = [
    { name = "claude", display_name = "claude" },
]
models = []
```

with:

```toml
providers = [
    { id = "nyt-litellm", name = "NYT LiteLLM Gateway", location = "cloud", protocols = ["openai-chat"], auth = { type = "api_key", base_url = "https://llm-gateway.nyt.net", secret_ref = "exec:/usr/local/bin/litellm-credential-helper.sh" } },
]
families = [
    { name = "claude", display_name = "claude" },
    { name = "claude-sonnet-4-6", display_name = "Claude Sonnet 4.6" },
]
models = [
    { id = "nyt-litellm/claude-sonnet-4-6", family = "claude-sonnet-4-6", provider_id = "nyt-litellm", model_name = "claude-sonnet-4-6", tags = ["code"] },
]
```

(Preserving the existing `claude` family entry since it's already there and removing it is out of scope for this change; only adding the new family and provider/model rows. The inline-table TOML form above is equivalent to the nested `[[providers]]`/`[[models]]` block form shown in the spec — either parses identically; this uses inline tables to keep the diff to the three top-level array assignments.)

- [ ] **Step 3: Write the wt config**

Create `~/.config/agent-wt/config.toml`:

```toml
default_tag = "code"

[[agents]]
name = "pi"
supported_providers = ["nyt-litellm"]
default_provider = "nyt-litellm"
```

- [ ] **Step 4: Validate the new config loads and passes validation**

Run: `~/.local/bin/wt config path` (confirms `wt` reads `~/.config/agent-wt/` — should print that directory)

Then run: `~/.local/bin/wt --version` (a cheap way to force a `config.Load()` + `Validate()` pass without launching anything — if `--version` exits 0 with a version string, the config parsed and validated cleanly; if `registry.toml` or `config.toml` has a syntax or validation error, `wt` will fail closed with a parse/validation error on essentially every subcommand per `wt/CLAUDE.md`'s config-loading notes).

Expected: prints a version string, no error. If it errors, re-check the TOML syntax in Steps 2-3 against the exact schema in `docs/contracts/registry.sample.toml` (read-only reference — do not edit this contract fixture).

- [ ] **Step 5: Remove the stale `anthropic` block from pi's models.json**

Read `~/.pi/agent/models.json`, remove the `"anthropic"` key from its `"providers"` object (leave any other pre-existing provider keys, if present, untouched — on this machine today the file has only `"anthropic"`, so the result is `{"providers": {}}`), and write it back. This can be done with `jq`:

```bash
jq 'del(.providers.anthropic)' ~/.pi/agent/models.json.bak-2026-09-23 > ~/.pi/agent/models.json
cat ~/.pi/agent/models.json
```

Expected output: `{"providers": {}}` (or equivalent with other keys preserved, if any exist beyond `anthropic` — confirm none do before proceeding, since this repo has no visibility into that file's exact live contents at plan-authoring time).

- [ ] **Step 6: Delete the old wrapper script**

```bash
rm ~/.pi/agent/activate-litellm.sh
```

- [ ] **Step 7: Grep for any shell config still invoking the old wrapper**

```bash
grep -rn "activate-litellm" ~/.zshrc ~/.zprofile ~/.bashrc ~/.bash_profile ~/.config/fish/config.fish 2>/dev/null
```

Expected: no matches, or matches that need manual follow-up. If any shell rc file defines an alias or function calling `activate-litellm.sh` (e.g. `alias pi='~/.pi/agent/activate-litellm.sh'`), do **not** edit that file automatically — report the exact line(s) found so the user can update it to `wt -A pi` (or a `pi-wt` shim, if `wt/bin/` provides one) by hand, per the spec's Section 5 note that shell rc files are not edited automatically as part of this change.

- [ ] **Step 8: No commit for this task**

Everything touched in this task lives outside the git worktree (`~/.config/...`, `~/.pi/...`) — there is nothing to `git add`/`git commit` here. Proceed directly to Task 5.

---

## Task 5: End-to-end manual verification

**Files:** none (verification only)

**Interfaces:**
- Consumes: everything from Tasks 1-4.
- Produces: confirmed working `wt -A pi` launch routed through `nyt-litellm`, and a confirmed clean failure path when the credential helper can't produce a key.

- [ ] **Step 1: Confirm the credential helper works standalone**

```bash
/usr/local/bin/litellm-credential-helper.sh
```

Expected: prints a single `sk-...` key to stdout (may trigger a Vault OIDC browser login first if not already authenticated — that's expected helper behavior, not a wt concern). If this fails here, stop and resolve the underlying Vault/`vault`-CLI/`jq` issue before proceeding — the remaining steps cannot succeed without a working helper.

- [ ] **Step 2: Launch pi through wt at the current repo root**

```bash
cd /Volumes/tempfs1/github/ohanaverse/local-ai-setup
~/.local/bin/wt --cwd -A pi
```

Expected: wt resolves the `nyt-litellm` provider (no agent/model picker needed — pi has exactly one supported provider and one model, so `wt` should proceed directly or offer a single-row pick), resolves the secret via the helper (possibly with a brief pause on the first call), and launches `pi` with `--model nyt-litellm/claude-sonnet-4-6`. Confirm pi starts and can complete a trivial round-trip (e.g. ask it "say ok" inside the pi session, confirm a response comes back with no 401/auth error), then exit pi.

- [ ] **Step 3: Confirm `models.json` was synced correctly**

```bash
cat ~/.pi/agent/models.json
```

Expected: a `"nyt-litellm"` provider block with `"baseUrl": "https://llm-gateway.nyt.net"`, a resolved (non-empty, `sk-...`-shaped) `"apiKey"`, one model entry `"id": "claude-sonnet-4-6"` with `"_launch": true`, and `"_wtOwned": true`. No `"anthropic"` key present.

- [ ] **Step 4: Confirm the failure path aborts cleanly**

Temporarily rename the helper to simulate it being unavailable, then attempt a launch:

```bash
sudo mv /usr/local/bin/litellm-credential-helper.sh /usr/local/bin/litellm-credential-helper.sh.disabled
~/.local/bin/wt --cwd -A pi
```

Expected: `wt` exits with an error mentioning the missing/failed helper (e.g. `exec: ... no such file or directory` wrapped with the `secret_ref "exec:..."` prefix from Task 1's error format) — pi must **not** be launched at all. Restore the helper immediately after observing the failure:

```bash
sudo mv /usr/local/bin/litellm-credential-helper.sh.disabled /usr/local/bin/litellm-credential-helper.sh
```

- [ ] **Step 5: Clean up backup files once satisfied**

```bash
rm ~/.pi/agent/models.json.bak-2026-09-23 ~/.pi/agent/activate-litellm.sh.bak-2026-09-23
```

(Only after confirming Steps 1-4 all behaved as expected — these backups are the rollback path if anything above needed a redo.)

- [ ] **Step 6: No commit for this task**

Verification only — nothing in the repo changed. This is the final task in the plan.
