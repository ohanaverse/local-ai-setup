# wt: local model inventory — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a Go package that discovers local models (pulled/on-disk, registered or not) and reports live running-state, without depending on modelman's `running` flags.

**Architecture:** New `wt/internal/localmodels` package with `Inventory(cfg) Snapshot`: one concurrent probe round per provider family (ollama via HTTP API, omlx/mtplx via directory scan + `/v1/models`, mlx_lm_server running-only), then a merge step that matches artifacts to registry models (registry id wins, else `config.DiscoveredModelID`). The registry parser starts reading each provider's `model_dir`. `localgate` keeps its policy but delegates its probe helpers to the new package.

**Tech Stack:** Go 1.26 (module root `wt/`), stdlib `net/http`, `httptest`, `testing`, BurntSushi/toml (already used).

**Spec:** `docs/superpowers/specs/2026-09-19-wt-local-model-inventory-design.md`

## Global Constraints

- Per-probe timeout is 2 seconds (`probeTimeout = 2 * time.Second`).
- Ollama discovery uses the HTTP API (`GET /api/tags`, `GET /api/ps`); no subprocess. Entries with a non-empty `remote_host` are cloud models and are excluded.
- Running-state is purely live. The inventory never reads modelman's `running` flag (`Config.RunningLocalModelIDs`).
- Default probe origins: ollama `config.OllamaBaseURL` (`http://localhost:11434`), omlx `http://localhost:8000`, mtplx `http://localhost:8003`, mlx_lm_server `http://localhost:8001`; overridden by the provider's registry `auth.base_url` (normalized with `config.BaseOrigin`).
- Default model dirs: omlx `~/.omlx/models`, mtplx `~/.mtplx/models`; overridden by the provider's registry `model_dir` (expanded with `config.ExpandHome`).
- mtplx directory `Youssofal--X` maps to repo id `Youssofal/X` (`strings.ReplaceAll(dir, "--", "/")`, every occurrence).
- Matching rule for omlx/mtplx: equal, or one is a `/`-suffix of the other (`NameMatches`); strict suffix so `-4bit` never matches `-8bit`. Ollama: exact, or `want` has no `:` tag and equals `have` minus `:latest`.
- Discovered-model id is `config.DiscoveredModelID(provider, artifact)`; a registry match keeps the registry id.
- `localgate.Apply` and its modelman-flag policy stay behaviorally unchanged; all existing `localgate` tests must keep passing.
- Every `Test*` needs a top-level `//` comment stating what it tests and why it matters (repo rule, `wt/CLAUDE.md`).
- Run Go commands from `wt/`. Run `go vet ./...` and `gofmt -l .` before each commit (wt-ci gates on gofmt).
- Commit messages during execution end with `- completes plan item #N` and the trailer `Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>`.
- Execute in an isolated worktree (superpowers:using-git-worktrees), never with `main` checked out in a linked worktree. Do not push or open a PR without asking.

## File Structure

| File | Responsibility |
|---|---|
| `wt/internal/config/config.go` | Modify: `Provider.ModelDir`; exported `ExpandHome` |
| `wt/internal/config/registry.go` | Modify: comment at ~line 79 (model_dir now decoded) |
| `wt/internal/config/registry_test.go` | Modify: new test |
| `wt/internal/localmodels/match.go` | Create: `NameMatches`, `OllamaNameMatches`, `FetchModelIDs` |
| `wt/internal/localmodels/match_test.go` | Create |
| `wt/internal/localgate/localgate.go` | Modify: delegate `nameMatches`/`fetchModelIDs` |
| `wt/internal/localmodels/sources.go` | Create: `ollamaModelNames`, `scanModelDirs`, `mtplxRepoID` |
| `wt/internal/localmodels/sources_test.go` | Create |
| `wt/internal/localmodels/inventory.go` | Create: types, `Inventory`, probing, merge |
| `wt/internal/localmodels/inventory_test.go` | Create |
| `wt/CLAUDE.md` | Modify: docs |

---

### Task 1: Registry parser reads `model_dir`; export `ExpandHome`

**Files:**
- Modify: `wt/internal/config/config.go` (`Provider` struct, ~line 207)
- Modify: `wt/internal/config/registry.go` (add exported wrapper near `expandHome`, ~line 62; fix the comment above `loadRegistry` ~line 79)
- Test: `wt/internal/config/registry_test.go`

**Interfaces:**
- Consumes: existing `expandHome`, `loadRegistry`, `writeRegistry` test helper.
- Produces: `Provider.ModelDir string` (`toml:"model_dir,omitempty"`, `~` not expanded); `func ExpandHome(path string) (string, error)`.

- [ ] **Step 1: Write the failing tests** (append to `registry_test.go`)

```go
// TestLoad_RegistryReadsProviderModelDirAndBaseURL verifies wt now decodes a
// provider's model_dir and auth.base_url from registry.toml. The local model
// inventory scans model_dir for on-disk models and probes base_url, so
// dropping either would silently point discovery at the wrong place.
func TestLoad_RegistryReadsProviderModelDirAndBaseURL(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	writeRegistry(t, dir, `
[[providers]]
id = "mtplx"
name = "MTPLX"
location = "local"
model_dir = "~/.mtplx/models"

[providers.auth]
type = "none"
base_url = "http://localhost:8003/v1"
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
	p := cfg.ProviderByID("mtplx")
	if p == nil {
		t.Fatal("mtplx provider not loaded")
	}
	if p.ModelDir != "~/.mtplx/models" {
		t.Errorf("ModelDir = %q, want %q", p.ModelDir, "~/.mtplx/models")
	}
	if p.Auth.BaseURL != "http://localhost:8003/v1" {
		t.Errorf("Auth.BaseURL = %q", p.Auth.BaseURL)
	}
}

// TestExpandHomeExported verifies the exported ExpandHome expands a leading
// ~/ against $HOME and leaves other paths alone — the inventory relies on it
// to turn a registry model_dir like "~/.omlx/models" into a real directory.
func TestExpandHomeExported(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	got, err := ExpandHome("~/a/b")
	if err != nil || got != filepath.Join(home, "a/b") {
		t.Errorf("ExpandHome(~/a/b) = %q, %v", got, err)
	}
	if got, _ := ExpandHome("/abs/path"); got != "/abs/path" {
		t.Errorf("ExpandHome(/abs/path) = %q", got)
	}
}
```
(Add `path/filepath` to the test file imports if missing.)

- [ ] **Step 2: Run to verify failure**

Run: `go test ./internal/config -run 'ProviderModelDir|ExpandHomeExported' -v`
Expected: FAIL to compile (`ModelDir`, `ExpandHome` undefined).

- [ ] **Step 3: Implement**

In `config.go`, add to `Provider` after `Auth`:
```go
	// ModelDir is the registry's model_dir (e.g. "~/.omlx/models"): where a
	// filesystem-backed provider keeps its models. Read-only; not expanded.
	ModelDir string `toml:"model_dir,omitempty"`
```
In `registry.go`, after `expandHome`, add:
```go
// ExpandHome expands a leading "~" or "~/" in path against the user's home
// directory; other paths are returned unchanged.
func ExpandHome(path string) (string, error) { return expandHome(path) }
```
In the `loadRegistry` doc comment change "(cost, model_info, fetch, and model_dir) are ignored" to "(cost, model_info, and fetch) are ignored; model_dir and auth fields are parsed into the provider data".

- [ ] **Step 4: Run to verify pass**

Run: `go test ./internal/config -v 2>&1 | tail -5 && go vet ./...`
Expected: PASS (existing `TestLoad_RegistryExtraFieldsIgnored` still passes: it only asserts the model loads).

- [ ] **Step 5: Commit**

```bash
gofmt -l internal
git add wt/internal/config
git commit -m "feat(config): read provider model_dir; export ExpandHome - completes plan item #1"
```

---

### Task 2: `localmodels` matching + probe helpers; `localgate` delegates

**Files:**
- Create: `wt/internal/localmodels/match.go`, `wt/internal/localmodels/match_test.go`
- Modify: `wt/internal/localgate/localgate.go` (`nameMatches` ~line 78, `fetchModelIDs` ~line 86)

**Interfaces:**
- Consumes: nothing new.
- Produces:
  - `func NameMatches(served, want string) bool`
  - `func OllamaNameMatches(have, want string) bool`
  - `func FetchModelIDs(client *http.Client, url string) []string` (nil on any failure)
  - `localgate.nameMatches` and `localgate.fetchModelIDs(url)` keep their current signatures and become thin delegations.

- [ ] **Step 1: Write the failing tests** (`match_test.go`)

```go
package localmodels

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// TestNameMatches pins the omlx/mtplx matching rule: lenient on the prefix (a
// server may spell the same model with a path/org prefix), strict on the
// variant tail — 4-bit and 8-bit variants differ exactly there, so a
// mismatched tail must be a different model, never a spelling variant.
func TestNameMatches(t *testing.T) {
	cases := []struct {
		served, want string
		match        bool
	}{
		{"Qwen3.8-27B-4bit", "Qwen3.8-27B-4bit", true},
		{"Qwen3.8-27B-4bit", "mlx-community/Qwen3.8-27B-4bit", true},
		{"mlx-community/Qwen3.8-27B-4bit", "Qwen3.8-27B-4bit", true},
		{"Qwen3.8-27B-4bit", "mlx-community/Qwen3.8-27B-8bit", false},
		{"other", "Qwen3.8-27B-4bit", false},
	}
	for _, tc := range cases {
		if got := NameMatches(tc.served, tc.want); got != tc.match {
			t.Errorf("NameMatches(%q, %q) = %v, want %v", tc.served, tc.want, got, tc.match)
		}
	}
}

// TestOllamaNameMatches verifies ollama matching is exact, with the ":latest"
// fallback ollama itself applies to a tagless name — so a registry entry
// "llama3" finds the pulled "llama3:latest", but "llama3:8b" never matches
// "llama3:70b".
func TestOllamaNameMatches(t *testing.T) {
	cases := []struct {
		have, want string
		match      bool
	}{
		{"gemma4:9b", "gemma4:9b", true},
		{"llama3:latest", "llama3", true},
		{"llama3:8b", "llama3", false},
		{"llama3:70b", "llama3:8b", false},
	}
	for _, tc := range cases {
		if got := OllamaNameMatches(tc.have, tc.want); got != tc.match {
			t.Errorf("OllamaNameMatches(%q, %q) = %v, want %v", tc.have, tc.want, got, tc.match)
		}
	}
}

// TestFetchModelIDs verifies the /v1/models probe returns served ids on 2xx
// and nil on every failure (bad status, bad JSON, unreachable) — nil must
// read as "nothing serving", never as an error a caller could mistake for
// "unknown".
func TestFetchModelIDs(t *testing.T) {
	ok := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"data":[{"id":"a"},{"id":""},{"id":"b"}]}`))
	}))
	defer ok.Close()
	client := &http.Client{Timeout: time.Second}
	if got := FetchModelIDs(client, ok.URL); len(got) != 2 || got[0] != "a" || got[1] != "b" {
		t.Errorf("ok = %v, want [a b]", got)
	}

	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Error(w, "x", 500) }))
	defer bad.Close()
	if got := FetchModelIDs(client, bad.URL); got != nil {
		t.Errorf("500 = %v, want nil", got)
	}
	junk := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte("not json")) }))
	defer junk.Close()
	if got := FetchModelIDs(client, junk.URL); got != nil {
		t.Errorf("junk = %v, want nil", got)
	}
	if got := FetchModelIDs(client, "http://127.0.0.1:1/"); got != nil {
		t.Errorf("unreachable = %v, want nil", got)
	}
}
```

- [ ] **Step 2: Run to verify failure**

Run: `go test ./internal/localmodels -v` — Expected: FAIL to compile (package has no non-test files).

- [ ] **Step 3: Implement** (`match.go`)

```go
// Package localmodels inventories local models: what is pulled or on disk
// (registered or not) and what is serving right now. Discovery is HTTP and
// filesystem only — wt never shells out for it — and running-state is a live
// probe, never modelman's per-model running flag.
package localmodels

import (
	"encoding/json"
	"net/http"
	"strings"
)

// NameMatches reports whether a served/on-disk model name names the same model
// as want. Lenient on prefix (a server may report a path- or org-prefixed
// spelling), strict on the variant tail — 4-bit and 8-bit variants differ
// exactly there, so a mismatched tail is a different model.
func NameMatches(served, want string) bool {
	return served == want || strings.HasSuffix(served, "/"+want) || strings.HasSuffix(want, "/"+served)
}

// OllamaNameMatches reports whether ollama's name have satisfies want: exact,
// or — when want carries no ":" tag — have is want with ollama's implicit
// ":latest" tag.
func OllamaNameMatches(have, want string) bool {
	if have == want {
		return true
	}
	return !strings.Contains(want, ":") && have == want+":latest"
}

// FetchModelIDs GETs an OpenAI-compatible /v1/models endpoint and returns the
// ids of the models the server is serving; nil on any failure (connection
// refused, timeout, non-2xx, non-JSON body) — nil reads as "nothing serving",
// never as "unknown".
func FetchModelIDs(client *http.Client, url string) []string {
	resp, err := client.Get(url)
	if err != nil {
		return nil
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil
	}
	var body struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return nil
	}
	ids := make([]string, 0, len(body.Data))
	for _, d := range body.Data {
		if d.ID != "" {
			ids = append(ids, d.ID)
		}
	}
	return ids
}
```
In `localgate.go`, replace the bodies (keep the doc comments, reduce them to one line saying they delegate to `localmodels`):
```go
func nameMatches(served, want string) bool { return localmodels.NameMatches(served, want) }

func fetchModelIDs(url string) []string { return localmodels.FetchModelIDs(httpClient, url) }
```
Add the `internal/localmodels` import; drop `encoding/json` and `strings` imports only if now unused (the compiler will say).

- [ ] **Step 4: Run to verify pass**

Run: `go test ./internal/localmodels ./internal/localgate -v 2>&1 | tail -15 && go vet ./...`
Expected: PASS, all pre-existing localgate tests included.

- [ ] **Step 5: Commit**

```bash
gofmt -l internal
git add wt/internal/localmodels wt/internal/localgate
git commit -m "feat(localmodels): matching + probe helpers; localgate delegates - completes plan item #2"
```

---

### Task 3: Discovery sources — ollama API and directory scan

**Files:**
- Create: `wt/internal/localmodels/sources.go`, `wt/internal/localmodels/sources_test.go`

**Interfaces:**
- Consumes: nothing from earlier tasks.
- Produces:
  - `func ollamaModelNames(client *http.Client, url string) ([]string, error)` — model names from an ollama `{"models":[{"name":...,"remote_host":...}]}` body (`/api/tags` or `/api/ps`), skipping entries with a non-empty `remote_host`; error on transport failure, non-2xx, or bad JSON.
  - `func scanModelDirs(dir string) ([]string, error)` — names of subdirectories of `dir` (symlinks to directories included, files skipped); missing dir returns `nil, nil`.
  - `func mtplxRepoID(dirName string) string`.

- [ ] **Step 1: Write the failing tests** (`sources_test.go`)

```go
package localmodels

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"testing"
	"time"
)

// TestOllamaModelNames verifies decoding of ollama's tags/ps body and that
// cloud entries (non-empty remote_host) are excluded — they are hosted at
// ollama.com, not local models, and would otherwise show up as local
// artifacts in the selector.
func TestOllamaModelNames(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"models":[
			{"name":"qwen3.8:27b-mlx"},
			{"name":"kimi-k3:cloud","remote_host":"https://ollama.com"},
			{"name":"medgemma:27b","remote_host":""}]}`))
	}))
	defer srv.Close()
	got, err := ollamaModelNames(&http.Client{Timeout: time.Second}, srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"qwen3.8:27b-mlx", "medgemma:27b"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

// TestOllamaModelNamesErrors verifies a non-2xx status, bad JSON and an
// unreachable daemon are all errors, so the inventory can report the ollama
// provider as unreachable instead of silently listing nothing.
func TestOllamaModelNamesErrors(t *testing.T) {
	client := &http.Client{Timeout: time.Second}
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Error(w, "x", 500) }))
	defer bad.Close()
	if _, err := ollamaModelNames(client, bad.URL); err == nil {
		t.Error("500: want error")
	}
	junk := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte("nope")) }))
	defer junk.Close()
	if _, err := ollamaModelNames(client, junk.URL); err == nil {
		t.Error("bad json: want error")
	}
	if _, err := ollamaModelNames(client, "http://127.0.0.1:1/"); err == nil {
		t.Error("unreachable: want error")
	}
}

// TestScanModelDirs verifies only directories count as models (loose files
// like .DS_Store do not), a symlink to a directory counts (users symlink
// model dirs into the cache), and a missing directory is "no models", not
// an error — a fresh install has no ~/.omlx/models yet.
func TestScanModelDirs(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "Qwen3.8-27B-4bit"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".DS_Store"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	target := t.TempDir()
	if err := os.Symlink(target, filepath.Join(root, "linked")); err != nil {
		t.Fatal(err)
	}
	got, err := scanModelDirs(root)
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(got)
	if want := []string{"Qwen3.8-27B-4bit", "linked"}; !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
	if got, err := scanModelDirs(filepath.Join(root, "missing")); got != nil || err != nil {
		t.Errorf("missing dir = %v, %v; want nil, nil", got, err)
	}
}

// TestMtplxRepoID verifies mtplx's "<org>--<model>" directory names map back
// to "org/model" — including every "--" — since the registry stores the
// slash form and matching/ids depend on it.
func TestMtplxRepoID(t *testing.T) {
	if got := mtplxRepoID("Youssofal--Qwen3.8-27B-MTPLX-Optimized-Quality"); got != "Youssofal/Qwen3.8-27B-MTPLX-Optimized-Quality" {
		t.Errorf("got %q", got)
	}
	if got := mtplxRepoID("org--sub--model"); got != "org/sub/model" {
		t.Errorf("got %q", got)
	}
	if got := mtplxRepoID("plain"); got != "plain" {
		t.Errorf("got %q", got)
	}
}
```

- [ ] **Step 2: Run to verify failure**

Run: `go test ./internal/localmodels -run 'OllamaModelNames|ScanModelDirs|MtplxRepoID' -v` — Expected: FAIL to compile.

- [ ] **Step 3: Implement** (`sources.go`)

```go
package localmodels

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

// ollamaModelNames returns the names in an ollama {"models":[...]} response
// (/api/tags: pulled models; /api/ps: loaded models). Entries with a non-empty
// remote_host are ollama.com cloud models, not local ones, and are skipped.
func ollamaModelNames(client *http.Client, url string) ([]string, error) {
	resp, err := client.Get(url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("GET %s: status %d", url, resp.StatusCode)
	}
	var body struct {
		Models []struct {
			Name       string `json:"name"`
			RemoteHost string `json:"remote_host"`
		} `json:"models"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return nil, fmt.Errorf("decode %s: %w", url, err)
	}
	names := make([]string, 0, len(body.Models))
	for _, m := range body.Models {
		if m.Name != "" && m.RemoteHost == "" {
			names = append(names, m.Name)
		}
	}
	return names, nil
}

// scanModelDirs lists the subdirectories of dir (symlinks to directories
// included). A missing dir is "no models", not an error.
func scanModelDirs(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var names []string
	for _, e := range entries {
		isDir := e.IsDir()
		if !isDir && e.Type()&fs.ModeSymlink != 0 {
			if st, err := os.Stat(filepath.Join(dir, e.Name())); err == nil && st.IsDir() {
				isDir = true
			}
		}
		if isDir {
			names = append(names, e.Name())
		}
	}
	return names, nil
}

// mtplxRepoID maps an MTPLX "<org>--<model>" directory name to "org/model"
// (every "--" becomes "/", matching modelman's _repo_id).
func mtplxRepoID(dirName string) string {
	return strings.ReplaceAll(dirName, "--", "/")
}
```

- [ ] **Step 4: Run to verify pass**

Run: `go test ./internal/localmodels -v 2>&1 | tail -15 && go vet ./...` — Expected: PASS.

- [ ] **Step 5: Commit**

```bash
gofmt -l internal
git add wt/internal/localmodels
git commit -m "feat(localmodels): ollama API and model-dir discovery sources - completes plan item #3"
```

---

### Task 4: `Inventory` — probing and merge

**Files:**
- Create: `wt/internal/localmodels/inventory.go`, `wt/internal/localmodels/inventory_test.go`

**Interfaces:**
- Consumes: `NameMatches`, `OllamaNameMatches`, `FetchModelIDs` (Task 2); `ollamaModelNames`, `scanModelDirs`, `mtplxRepoID` (Task 3); `config.Provider.ModelDir`, `config.ExpandHome` (Task 1); `config.DiscoveredModelID`, `config.BaseOrigin`, `config.OllamaBaseURL`, `(*config.Config).ProviderByID`, `ResolveLocation`.
- Produces:
  - `type Status string` with `StatusOK = "ok"`, `StatusUnreachable = "unreachable"`, `StatusUnsupported = "unsupported"`
  - `type Entry struct { ProviderID, Artifact, ModelID string; Registered, Running bool }`
  - `type Snapshot struct { Entries []Entry; Providers map[string]Status }` (`Providers` keyed by provider *family*: `ollama`, `omlx` (covers `omlx-6bit`), `mtplx`, `mlx_lm_server`)
  - `func Inventory(cfg *config.Config) Snapshot`
  - unexported `inventory(cfg *config.Config, client *http.Client) Snapshot` (tests call this)

- [ ] **Step 1: Write the failing tests** (`inventory_test.go`)

```go
package localmodels

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
)

var testClient = &http.Client{Timeout: 2 * time.Second}

// modelsServer serves an OpenAI-compatible /v1/models body.
func modelsServer(t *testing.T, ids ...string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		data := []map[string]string{}
		for _, id := range ids {
			data = append(data, map[string]string{"id": id})
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": data})
	}))
	t.Cleanup(srv.Close)
	return srv
}

// ollamaServer serves /api/tags (pulled) and /api/ps (loaded).
func ollamaServer(t *testing.T, pulled, loaded []string) *httptest.Server {
	t.Helper()
	body := func(names []string) map[string]any {
		ms := []map[string]string{}
		for _, n := range names {
			ms = append(ms, map[string]string{"name": n})
		}
		return map[string]any{"models": ms}
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/api/tags", func(w http.ResponseWriter, r *http.Request) { _ = json.NewEncoder(w).Encode(body(pulled)) })
	mux.HandleFunc("/api/ps", func(w http.ResponseWriter, r *http.Request) { _ = json.NewEncoder(w).Encode(body(loaded)) })
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func localProvider(id, baseURL, modelDir string) config.Provider {
	return config.Provider{ID: id, Location: config.LocationLocal, ModelDir: modelDir, Auth: config.AuthConfig{Type: "none", BaseURL: baseURL}}
}

func mkdirs(t *testing.T, root string, names ...string) {
	t.Helper()
	for _, n := range names {
		if err := os.Mkdir(filepath.Join(root, n), 0o755); err != nil {
			t.Fatal(err)
		}
	}
}

func byModelID(s Snapshot, id string) (Entry, bool) {
	for _, e := range s.Entries {
		if e.ModelID == id {
			return e, true
		}
	}
	return Entry{}, false
}

// TestInventoryOllamaDiscoveredAndRunning verifies pulled-but-unregistered
// ollama models are listed with DiscoveredModelID ids, and that RUNNING comes
// from /api/ps (loaded set) — not from anything modelman flags.
func TestInventoryOllamaDiscoveredAndRunning(t *testing.T) {
	srv := ollamaServer(t, []string{"gemma4:9b", "qwen3:8b"}, []string{"qwen3:8b"})
	cfg := &config.Config{Providers: []config.Provider{localProvider("ollama", srv.URL, "")}}
	snap := inventory(cfg, testClient)

	g, ok := byModelID(snap, config.DiscoveredModelID("ollama", "gemma4:9b"))
	if !ok || g.Registered || g.Running || g.Artifact != "gemma4:9b" {
		t.Errorf("gemma4 = %+v ok=%v, want discovered, not running", g, ok)
	}
	q, ok := byModelID(snap, config.DiscoveredModelID("ollama", "qwen3:8b"))
	if !ok || !q.Running {
		t.Errorf("qwen3 = %+v ok=%v, want running", q, ok)
	}
	if snap.Providers["ollama"] != StatusOK {
		t.Errorf("status = %q", snap.Providers["ollama"])
	}
}

// TestInventoryRegisteredMatchKeepsRegistryID verifies an on-disk oMLX dir that
// matches a registry model (dir "Qwen3.8-27B-4bit" vs model_name
// "mlx-community/Qwen3.8-27B-4bit") is reported under the REGISTRY id and marked
// registered, while a sibling 8-bit dir with no registry entry is discovered —
// so usage/survey history stays keyed on the registry id (sub-project 1).
func TestInventoryRegisteredMatchKeepsRegistryID(t *testing.T) {
	root := t.TempDir()
	mkdirs(t, root, "Qwen3.8-27B-4bit", "Qwen3.8-27B-8bit")
	srv := modelsServer(t, "Qwen3.8-27B-4bit")
	cfg := &config.Config{
		Providers: []config.Provider{localProvider("omlx", srv.URL, root)},
		Models: []config.Model{{ID: "omlx/mlx-community--Qwen3.8-27B-4bit", ProviderID: "omlx", ModelName: "mlx-community/Qwen3.8-27B-4bit"}},
	}
	snap := inventory(cfg, testClient)

	reg, ok := byModelID(snap, "omlx/mlx-community--Qwen3.8-27B-4bit")
	if !ok || !reg.Registered || reg.Artifact != "Qwen3.8-27B-4bit" || !reg.Running {
		t.Errorf("registered = %+v ok=%v", reg, ok)
	}
	disc, ok := byModelID(snap, config.DiscoveredModelID("omlx", "Qwen3.8-27B-8bit"))
	if !ok || disc.Registered || disc.Running {
		t.Errorf("discovered 8bit = %+v ok=%v, want unregistered, not running", disc, ok)
	}
	if len(snap.Entries) != 2 {
		t.Errorf("entries = %d, want 2 (4bit must not also appear as discovered)", len(snap.Entries))
	}
}

// TestInventoryMtplxRepoIDMapping verifies an mtplx dir "Org--Model" is listed
// as artifact/id "Org/Model" and reads running when /v1/models serves that repo
// id — matching how the registry spells mtplx models.
func TestInventoryMtplxRepoIDMapping(t *testing.T) {
	root := t.TempDir()
	mkdirs(t, root, "Youssofal--Qwen3.8-27B-MTPLX-Optimized-Quality")
	srv := modelsServer(t, "Youssofal/Qwen3.8-27B-MTPLX-Optimized-Quality")
	cfg := &config.Config{Providers: []config.Provider{localProvider("mtplx", srv.URL+"/v1", root)}}
	snap := inventory(cfg, testClient)

	e, ok := byModelID(snap, "mtplx/Youssofal/Qwen3.8-27B-MTPLX-Optimized-Quality")
	if !ok || e.Artifact != "Youssofal/Qwen3.8-27B-MTPLX-Optimized-Quality" || !e.Running {
		t.Errorf("entry = %+v ok=%v", e, ok)
	}
}

// TestInventoryRegisteredMissingFromDisk verifies a configured local model with
// no matching artifact still appears (registered, not running, empty Artifact)
// so the selector can show a configured model that is missing from disk.
func TestInventoryRegisteredMissingFromDisk(t *testing.T) {
	srv := ollamaServer(t, []string{"other:1b"}, nil)
	cfg := &config.Config{
		Providers: []config.Provider{localProvider("ollama", srv.URL, "")},
		Models:    []config.Model{{ID: "ollama/gone:7b", ProviderID: "ollama", ModelName: "gone:7b"}},
	}
	e, ok := byModelID(inventory(cfg, testClient), "ollama/gone:7b")
	if !ok || !e.Registered || e.Running || e.Artifact != "" {
		t.Errorf("entry = %+v ok=%v", e, ok)
	}
}

// TestInventoryOllamaLatestFallback verifies a tagless registry name finds the
// pulled ":latest" model (ollama resolves it that way), so it is not listed
// twice — once registered and once as a discovered duplicate.
func TestInventoryOllamaLatestFallback(t *testing.T) {
	srv := ollamaServer(t, []string{"llama3:latest"}, []string{"llama3:latest"})
	cfg := &config.Config{
		Providers: []config.Provider{localProvider("ollama", srv.URL, "")},
		Models:    []config.Model{{ID: "ollama/llama3", ProviderID: "ollama", ModelName: "llama3"}},
	}
	snap := inventory(cfg, testClient)
	e, ok := byModelID(snap, "ollama/llama3")
	if !ok || e.Artifact != "llama3:latest" || !e.Running || len(snap.Entries) != 1 {
		t.Errorf("entry = %+v ok=%v entries=%d", e, ok, len(snap.Entries))
	}
}

// TestInventoryProviderDownDoesNotFailOthers verifies a dead ollama daemon is
// reported "unreachable" (its registered models still list, not running) while
// omlx discovery is unaffected — one provider must never blank the whole
// selector.
func TestInventoryProviderDownDoesNotFailOthers(t *testing.T) {
	dead := ollamaServer(t, nil, nil)
	deadURL := dead.URL
	dead.Close()
	root := t.TempDir()
	mkdirs(t, root, "Qwen3.8-27B-4bit")
	up := modelsServer(t)
	cfg := &config.Config{
		Providers: []config.Provider{localProvider("ollama", deadURL, ""), localProvider("omlx", up.URL, root)},
		Models:    []config.Model{{ID: "ollama/x:1b", ProviderID: "ollama", ModelName: "x:1b"}},
	}
	snap := inventory(cfg, testClient)
	if snap.Providers["ollama"] != StatusUnreachable || snap.Providers["omlx"] != StatusOK {
		t.Errorf("statuses = %v", snap.Providers)
	}
	if e, ok := byModelID(snap, "ollama/x:1b"); !ok || e.Running {
		t.Errorf("registered ollama entry = %+v ok=%v", e, ok)
	}
	if _, ok := byModelID(snap, config.DiscoveredModelID("omlx", "Qwen3.8-27B-4bit")); !ok {
		t.Error("omlx discovery missing")
	}
}

// TestInventoryOmlx6bitRowSharesServerAndDirs verifies a registered omlx-6bit
// model matches a dir in the shared oMLX model directory and reads running from
// the shared server, with no duplicate discovered entry (omlx and omlx-6bit are
// one physical server).
func TestInventoryOmlx6bitRowSharesServerAndDirs(t *testing.T) {
	root := t.TempDir()
	mkdirs(t, root, "Ornith-1.5-35B-A3B-MLX-6bit")
	srv := modelsServer(t, "Ornith-1.5-35B-A3B-MLX-6bit")
	cfg := &config.Config{
		Providers: []config.Provider{localProvider("omlx", srv.URL, root), localProvider("omlx-6bit", srv.URL, "")},
		Models:    []config.Model{{ID: "omlx-6bit/Ornith-1.5-35B-A3B-MLX-6bit", ProviderID: "omlx-6bit", ModelName: "Ornith-1.5-35B-A3B-MLX-6bit"}},
	}
	snap := inventory(cfg, testClient)
	if len(snap.Entries) != 1 || !snap.Entries[0].Registered || !snap.Entries[0].Running || snap.Entries[0].ProviderID != "omlx-6bit" {
		t.Errorf("entries = %+v", snap.Entries)
	}
}

// TestInventoryMlxLMServerRunningNoDiscovery verifies mlx_lm_server is
// "unsupported" for discovery (a target+draft pairing is not discoverable) yet a
// registered row reads running when the server serves anything.
func TestInventoryMlxLMServerRunningNoDiscovery(t *testing.T) {
	srv := modelsServer(t, "/some/target/path")
	cfg := &config.Config{
		Providers: []config.Provider{localProvider("mlx_lm_server", srv.URL, "")},
		Models:    []config.Model{{ID: "mlx_lm_server/x", ProviderID: "mlx_lm_server", ModelName: "x"}},
	}
	snap := inventory(cfg, testClient)
	if snap.Providers["mlx_lm_server"] != StatusUnsupported {
		t.Errorf("status = %q", snap.Providers["mlx_lm_server"])
	}
	if len(snap.Entries) != 1 || !snap.Entries[0].Running {
		t.Errorf("entries = %+v", snap.Entries)
	}
}

// TestInventoryIgnoresCloudModelsAndProviders verifies cloud-located providers
// and models are never probed or listed — the inventory is local-only.
func TestInventoryIgnoresCloudModelsAndProviders(t *testing.T) {
	cfg := &config.Config{
		Providers: []config.Provider{{ID: "openrouter", Location: config.LocationCloud}, {ID: "ollama", Location: config.LocationCloud}},
		Models:    []config.Model{{ID: "openrouter/x", ProviderID: "openrouter", ModelName: "x"}, {ID: "ollama/k:cloud", ProviderID: "ollama", ModelName: "k:cloud", Location: config.LocationCloud}},
	}
	snap := inventory(cfg, testClient)
	if len(snap.Entries) != 0 || len(snap.Providers) != 0 {
		t.Errorf("snapshot = %+v, want empty", snap)
	}
}

// TestInventoryProbesProvidersConcurrently verifies the probe round runs the
// providers in parallel: each server holds its response until BOTH have been
// hit, so a sequential implementation would take ~1.5s per provider while a
// concurrent one finishes well under a second. This keeps the selector's open
// latency at one probe timeout, not the sum of all of them.
func TestInventoryProbesProvidersConcurrently(t *testing.T) {
	var hits int32
	barrier := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			atomic.AddInt32(&hits, 1)
			deadline := time.Now().Add(1500 * time.Millisecond)
			for atomic.LoadInt32(&hits) < 2 && time.Now().Before(deadline) {
				time.Sleep(5 * time.Millisecond)
			}
			next.ServeHTTP(w, r)
		})
	}
	ollama := httptest.NewServer(barrier(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"models":[]}`))
	})))
	defer ollama.Close()
	omlx := httptest.NewServer(barrier(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"data":[]}`))
	})))
	defer omlx.Close()
	cfg := &config.Config{Providers: []config.Provider{
		localProvider("ollama", ollama.URL, ""), localProvider("omlx", omlx.URL, t.TempDir()),
	}}
	start := time.Now()
	inventory(cfg, testClient)
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Errorf("inventory took %v, want < 1s (providers must probe concurrently)", elapsed)
	}
}
```

- [ ] **Step 2: Run to verify failure**

Run: `go test ./internal/localmodels -run Inventory -v` — Expected: FAIL to compile (`inventory`, `Snapshot`, ... undefined).

- [ ] **Step 3: Implement** (`inventory.go`)

```go
package localmodels

import (
	"net/http"
	"sort"
	"sync"
	"time"

	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
)

// Status is one provider family's probe outcome.
type Status string

const (
	StatusOK          Status = "ok"
	StatusUnreachable Status = "unreachable"
	// StatusUnsupported marks a provider whose models cannot be discovered
	// (mlx_lm_server's target+draft pairing); only running-state is probed.
	StatusUnsupported Status = "unsupported"
)

// probeTimeout bounds each HTTP probe.
const probeTimeout = 2 * time.Second

const (
	defaultOmlxOrigin  = "http://localhost:8000"
	defaultMlxLMOrigin = "http://localhost:8001"
	defaultMtplxOrigin = "http://localhost:8003"
	defaultOmlxDir     = "~/.omlx/models"
	defaultMtplxDir    = "~/.mtplx/models"
)

// Entry is one local model: registered (Registered) or discovered.
type Entry struct {
	ProviderID string // registry provider id (omlx-6bit rows keep theirs); the family id for discovered entries
	Artifact   string // pulled/on-disk name in the provider's spelling; "" for a registered model not found on disk
	ModelID    string // registry id when registered, else config.DiscoveredModelID(ProviderID, Artifact)
	Registered bool
	Running    bool // serving right now (live probe only)
}

// Snapshot is one inventory round. Providers is keyed by provider family
// (ollama, omlx — which also covers omlx-6bit — mtplx, mlx_lm_server).
type Snapshot struct {
	Entries   []Entry
	Providers map[string]Status
}

// Inventory probes every local provider in the registry concurrently and
// merges discovered artifacts with the registry's local models.
func Inventory(cfg *config.Config) Snapshot {
	return inventory(cfg, &http.Client{Timeout: probeTimeout})
}

// familyOf maps a registry provider id to its probe family; "" when wt has no
// probe for it (e.g. retired llamacpp). omlx and omlx-6bit are ONE physical
// server, so they share the "omlx" family.
func familyOf(providerID string) string {
	switch providerID {
	case "ollama", "omlx", "mtplx", "mlx_lm_server":
		return providerID
	case "omlx-6bit":
		return "omlx"
	}
	return ""
}

// source is one family's probe result.
type source struct {
	family    string
	status    Status
	artifacts []string // discovered names, provider spelling (mtplx: repo id form)
	loaded    []string // names serving right now
}

func (s *source) matchArtifact(artifact, modelName string) bool {
	switch s.family {
	case "ollama":
		return OllamaNameMatches(artifact, modelName)
	case "omlx", "mtplx":
		return NameMatches(artifact, modelName)
	}
	return false
}

func (s *source) isRunning(name string) bool {
	switch s.family {
	case "ollama":
		for _, l := range s.loaded {
			if OllamaNameMatches(l, name) {
				return true
			}
		}
	case "omlx", "mtplx":
		for _, l := range s.loaded {
			if NameMatches(l, name) {
				return true
			}
		}
	case "mlx_lm_server":
		// One target+draft pairing per process, model loaded before serving:
		// a non-empty /v1/models is already model-accurate.
		return len(s.loaded) > 0
	}
	return false
}

// familyOrigin is the probe origin for a family: the first registry provider
// row of the family with an auth.base_url, else the default port.
func familyOrigin(cfg *config.Config, family string) string {
	ids, def := []string{family}, ""
	switch family {
	case "ollama":
		def = config.OllamaBaseURL
	case "omlx":
		ids, def = []string{"omlx", "omlx-6bit"}, defaultOmlxOrigin
	case "mtplx":
		def = defaultMtplxOrigin
	case "mlx_lm_server":
		def = defaultMlxLMOrigin
	}
	for _, id := range ids {
		if p := cfg.ProviderByID(id); p != nil && p.Auth.BaseURL != "" {
			return config.BaseOrigin(p.Auth.BaseURL)
		}
	}
	return def
}

func probeFamily(cfg *config.Config, client *http.Client, family string) *source {
	s := &source{family: family, status: StatusOK}
	origin := familyOrigin(cfg, family)
	switch family {
	case "ollama":
		names, err := ollamaModelNames(client, origin+"/api/tags")
		if err != nil {
			s.status = StatusUnreachable
			return s
		}
		s.artifacts = names
		if loaded, err := ollamaModelNames(client, origin+"/api/ps"); err == nil {
			s.loaded = loaded
		}
	case "omlx", "mtplx":
		s.loaded = FetchModelIDs(client, origin+"/v1/models")
		p, def := cfg.ProviderByID(family), defaultOmlxDir
		if family == "mtplx" {
			def = defaultMtplxDir
		}
		// Directories are attributed to the "omlx" provider only; a config
		// with just a hand-added omlx-6bit row does not scan.
		if p == nil {
			return s
		}
		dirSetting := p.ModelDir
		if dirSetting == "" {
			dirSetting = def
		}
		dir, err := config.ExpandHome(dirSetting)
		if err != nil {
			s.status = StatusUnreachable
			return s
		}
		names, err := scanModelDirs(dir)
		if err != nil {
			s.status = StatusUnreachable
			return s
		}
		if family == "mtplx" {
			for i, n := range names {
				names[i] = mtplxRepoID(n)
			}
		}
		s.artifacts = names
	case "mlx_lm_server":
		s.status = StatusUnsupported
		s.loaded = FetchModelIDs(client, origin+"/v1/models")
	}
	return s
}

func inventory(cfg *config.Config, client *http.Client) Snapshot {
	var families []string
	seen := map[string]bool{}
	for _, p := range cfg.Providers {
		f := familyOf(p.ID)
		if f == "" || p.Location != config.LocationLocal || seen[f] {
			continue
		}
		seen[f] = true
		families = append(families, f)
	}

	results := make([]*source, len(families))
	var wg sync.WaitGroup
	for i, f := range families {
		wg.Add(1)
		go func(i int, f string) {
			defer wg.Done()
			results[i] = probeFamily(cfg, client, f)
		}(i, f)
	}
	wg.Wait()

	snap := Snapshot{Providers: map[string]Status{}}
	sources := map[string]*source{}
	for i, f := range families {
		sources[f] = results[i]
		snap.Providers[f] = results[i].status
	}

	consumed := map[string]bool{}
	for _, m := range cfg.Models {
		if loc, err := cfg.ResolveLocation(m); err != nil || loc != config.LocationLocal {
			continue
		}
		e := Entry{ProviderID: m.ProviderID, ModelID: m.ID, Registered: true}
		if src := sources[familyOf(m.ProviderID)]; src != nil {
			for _, a := range src.artifacts {
				key := src.family + "\x00" + a
				if !consumed[key] && src.matchArtifact(a, m.ModelName) {
					consumed[key] = true
					e.Artifact = a
					break
				}
			}
			name := e.Artifact
			if name == "" {
				name = m.ModelName
			}
			e.Running = src.isRunning(name)
		}
		snap.Entries = append(snap.Entries, e)
	}
	for _, f := range families {
		src := sources[f]
		for _, a := range src.artifacts {
			if consumed[f+"\x00"+a] {
				continue
			}
			snap.Entries = append(snap.Entries, Entry{
				ProviderID: f,
				Artifact:   a,
				ModelID:    config.DiscoveredModelID(f, a),
				Running:    src.isRunning(a),
			})
		}
	}
	sort.SliceStable(snap.Entries, func(i, j int) bool {
		a, b := snap.Entries[i], snap.Entries[j]
		if a.ProviderID != b.ProviderID {
			return a.ProviderID < b.ProviderID
		}
		return a.ModelID < b.ModelID
	})
	return snap
}
```

- [ ] **Step 4: Run to verify pass**

Run: `go test ./internal/localmodels -v 2>&1 | tail -25 && go vet ./... && go test ./...`
Expected: all PASS. If `TestInventoryProbesProvidersConcurrently` is flaky on a loaded machine, report it rather than loosening the bound below the 1s / 1.5s design.

- [ ] **Step 5: Commit**

```bash
gofmt -l internal
git add wt/internal/localmodels
git commit -m "feat(localmodels): Inventory - concurrent discovery and live running-state - completes plan item #4"
```

---

### Task 5: Documentation

**Files:**
- Modify: `wt/CLAUDE.md`

- [ ] **Step 1: Edit `wt/CLAUDE.md`**
  - Package list line (`internal/{config,rotation,usage,...,smoke}`): add `localmodels`.
  - Package table: add row `internal/localmodels/` — "Local model inventory: `Inventory(cfg)` probes ollama (`/api/tags` + `/api/ps`, cloud `remote_host` entries excluded), omlx/mtplx (model-dir scan + `/v1/models`) and mlx_lm_server (running only) concurrently and returns registered + discovered entries with live `Running`; registry match keeps the registry id, else `config.DiscoveredModelID`. Never reads modelman's `running` flag. `localgate` delegates its `nameMatches`/`fetchModelIDs` here."
  - Registry section: the sentence listing fields wt ignores ("model_info, fetch, model_dir, auth secret_ref/base_url") — `model_dir` and `auth.base_url` are now decoded (`Provider.ModelDir`, `Auth.BaseURL`); fix the wording so only `model_info` and `fetch` are described as ignored.
  - The "Lazy: … wt never shells out for discovery" note: append that `localmodels.Inventory` performs HTTP and filesystem discovery only (still no subprocess) and is not yet wired into a launch path (sub-project 3 consumes it).

- [ ] **Step 2: Verify**

Run (from repo root): `make check-links && git grep -n "localmodels" wt/CLAUDE.md`
Expected: links OK; matches in the package list, table row and lazy note.

- [ ] **Step 3: Final verification**

Run (from `wt/`): `go build ./... && go vet ./... && go test -count=1 ./... && gofmt -l .`
Expected: all pass; `gofmt -l` prints nothing.

- [ ] **Step 4: Commit**

```bash
git add wt/CLAUDE.md
git commit -m "docs(wt): document localmodels inventory - completes plan item #5"
```

---

## Self-Review

- **Spec coverage:** package + `Snapshot` shape, `Registered` entries missing on disk, per-provider status, failure isolation -> Task 4; helper move + `localgate` unchanged -> Task 2; ollama HTTP API + `remote_host` exclusion, dir scan, mtplx mapping -> Task 3; registry `model_dir`/`base_url` -> Task 1 (base_url already decoded; test pins it); matching rule, `:latest` fallback, registry-id-vs-Discovered ids -> Tasks 2 and 4; live-only running (no `RunningLocalModelIDs`) -> Task 4 (asserted by every running test using servers only); concurrency -> Task 4 test; docs -> Task 5. Out-of-scope items (selector, start, smoke, removing `localgate`) are untouched.
- **Refinement vs spec text:** the spec says on-disk oMLX directories are attributed to `omlx`. The plan does so for *discovered* entries (family id `omlx`), but lets a *registered* `omlx-6bit` row claim a matching dir (test `...Omlx6bitRowSharesServerAndDirs`) so it isn't shown twice. Flag at review.
- **Placeholders:** none; every code step has full code.
- **Type consistency:** `Entry`, `Snapshot`, `Status`, `NameMatches`, `OllamaNameMatches`, `FetchModelIDs(client, url)`, `ollamaModelNames(client, url)`, `scanModelDirs(dir)`, `mtplxRepoID`, `inventory(cfg, client)` are named identically in all tasks that use them.
