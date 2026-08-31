# Three-Repo Monorepo Consolidation Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Merge `modelman` and `agent-worktree` into `local-ai-setup` as subdirectories (`modelman/`, `wt/`), and add CI plus shared contract-test fixtures so a schema change to a file one language writes and another reads fails both languages' tests in the same PR, replacing the hand-maintained cross-repo tracker doc.

**Architecture:** `git subtree add` pulls each source repo's full history into a subdirectory of `local-ai-setup` in one merge commit. The Go module path is renamed to live under the new tree. New path-filtered GitHub Actions workflows run each subdirectory's existing test suite, plus a new `docs/contracts/` directory holding fixture files that both a Go test and a Python test load and assert against — editing a fixture (which happens when a shared schema changes) triggers both CI jobs.

**Tech Stack:** git subtree, GitHub Actions, Go 1.26.7 + `testing` + `BurntSushi/toml`, Python 3.13 + `uv` + `pytest` + `tomllib`.

**Spec:** `docs/superpowers/specs/2026-08-31-three-repo-monorepo-consolidation-design.md`

## Global Constraints

- Do not rewrite `wt` (Go) or `modelman` (Python) into the same language — out of scope per the spec's Non-goals.
- Do not change the registry/exposure/launch ownership split (modelman owns registry+exposure, wt is a read-only consumer) — only add the missing cross-repo safety net.
- Archive the two source repos when done; never delete them.
- Use `git subtree` for the merges (built into git). Do not install or use `git-filter-repo`.
- `wt-ci` must run on `macos-latest` — the codesign step (`make install` in `wt/`) is macOS-only.
- `docs/contracts/**` must be a trigger path in both `modelman-ci.yml` and `wt-ci.yml` — this is the mechanism that makes a shared-schema change fail both CI jobs in one PR.
- Never commit directly to `main` — this repo's `block-main-commit` git hook rejects it; all work happens on a feature branch (the executing skill sets this up).
- Never push a branch or open a PR, and never archive a GitHub repo, without the user's explicit go-ahead immediately before doing it (per this user's standing PR-discipline and destructive-action rules) — Tasks 12 and 13 below are gated on this explicitly.

---

### Task 1: Pre-migration safety checks

Read-only checks against the two source repos, run from their existing local clones — nothing here touches `local-ai-setup`.

**Files:** none created or modified; this task only reads state.

**Interfaces:**
- Produces: a go/no-go decision for Task 2 and Task 3. If anything below is dirty, stop and resolve it (commit, merge, or explicitly note it) before continuing — do not merge a repo mid-feature.

- [ ] **Step 1: Check `modelman` for unmerged branches, worktrees, and uncommitted changes**

```bash
git -C /Users/keith/github/ohanaverse/modelman status
git -C /Users/keith/github/ohanaverse/modelman worktree list
git -C /Users/keith/github/ohanaverse/modelman branch -vv
```

Expected: `status` clean (or only expected untracked build artifacts like `.venv`, `.pytest_cache`); `worktree list` shows only the main worktree (or any listed worktrees have no uncommitted work — check each); `branch -vv` shows no local branch ahead of its remote tracking branch, or any that are get named and resolved before proceeding.

- [ ] **Step 2: Check `agent-worktree` the same way**

```bash
git -C /Users/keith/github/ohanaverse/agent-worktree status
git -C /Users/keith/github/ohanaverse/agent-worktree worktree list
git -C /Users/keith/github/ohanaverse/agent-worktree branch -vv
```

Same expectations as Step 1.

- [ ] **Step 3: Confirm the `wt-litellm-gateway` plan is landed or explicitly paused**

```bash
git -C /Users/keith/github/ohanaverse/agent-worktree log --oneline --all --grep="litellm-gateway" -i
gh pr list --repo ohanaverse/agent-worktree --state all --search "litellm gateway"
```

Expected: the referenced plan (`agent-worktree/docs/superpowers/plans/2026-08-30-wt-litellm-gateway.md`) is either merged to `main` or there is no open PR/branch actively implementing it. If there is live in-progress work, stop and ask the user whether to land it first or explicitly proceed with it mid-merge.

- [ ] **Step 4: Scan for anything secret in the two histories that hasn't already been through a rotate/accept decision**

```bash
git -C /Users/keith/github/ohanaverse/modelman log -p --all | grep -iE "api[_-]?key|secret|password|token" | grep -v "secret_ref\|api_key.*=.*type\|SecretRef\|test" | head -50
git -C /Users/keith/github/ohanaverse/agent-worktree log -p --all | grep -iE "api[_-]?key|secret|password|token" | grep -v "secret_ref\|api_key.*=.*type\|SecretRef\|test" | head -50
```

Expected: no hits, or any hits are field names/test fixtures (not real credential values). If a real secret value turns up, stop and handle it the same way `local-ai-setup/issues.md` item 1 was handled (rotate, decide on history rewrite vs. accept) before merging that history into the new monorepo.

No commit — this task only gathers information for the go/no-go decision.

---

### Task 2: Subtree-merge `modelman` into `local-ai-setup`

**Files:**
- Create: `modelman/**` (the entire contents of the `modelman` repo, unchanged)

**Interfaces:**
- Consumes: Task 1's go/no-go confirmation.
- Produces: a `modelman/` subdirectory in `local-ai-setup` whose tree is byte-identical to the source repo's `main` HEAD, with full history preserved underneath. Task 4 (path fixes) and Task 6 (fixtures) build on this existing at `modelman/`.

- [ ] **Step 1: Run the subtree merge**

From the `local-ai-setup` working tree, on the feature branch the executing skill set up (not `main`):

```bash
git subtree add --prefix=modelman git@github.com:ohanaverse/modelman.git main
```

Expected: a single new commit `Add 'modelman/' from commit '<sha>'` (or similar, git's exact wording varies by version) with `modelman/` populated.

- [ ] **Step 2: Verify the merged tree is byte-identical to the source**

```bash
git clone --quiet git@github.com:ohanaverse/modelman.git /tmp/modelman-verify
diff -rq /tmp/modelman-verify --exclude=.git modelman/
rm -rf /tmp/modelman-verify
```

Expected: no output from `diff -rq` (no differences). If differences appear, stop — do not proceed to Task 3 with an unverified merge.

- [ ] **Step 3: Confirm history is walkable**

```bash
git log --oneline --follow -- modelman/src/modelman/registry.py | head -5
```

Expected: shows commits predating the subtree-merge commit (i.e., modelman's own history, not just the single merge commit).

No further commit needed — `git subtree add` already committed in Step 1.

---

### Task 3: Subtree-merge `agent-worktree` into `local-ai-setup` as `wt/`

**Files:**
- Create: `wt/**` (the entire contents of the `agent-worktree` repo, unchanged)

**Interfaces:**
- Consumes: Task 1's go/no-go confirmation. Independent of Task 2 (different path prefix, no shared files) — order between Task 2 and Task 3 doesn't matter, but both must complete before Task 4.
- Produces: a `wt/` subdirectory whose tree is byte-identical to `agent-worktree`'s `main` HEAD, with full history preserved. Task 4 renames the Go module inside this directory.

- [ ] **Step 1: Run the subtree merge**

```bash
git subtree add --prefix=wt git@github.com:ohanaverse/agent-worktree.git main
```

- [ ] **Step 2: Verify the merged tree is byte-identical to the source**

```bash
git clone --quiet git@github.com:ohanaverse/agent-worktree.git /tmp/agent-worktree-verify
diff -rq /tmp/agent-worktree-verify --exclude=.git wt/
rm -rf /tmp/agent-worktree-verify
```

Expected: no output. Stop if anything differs.

- [ ] **Step 3: Confirm history is walkable**

```bash
git log --oneline --follow -- wt/internal/config/registry.go | head -5
```

Expected: shows commits predating the subtree-merge commit.

No further commit needed.

---

### Task 4: Rename the Go module path

**Files:**
- Modify: `wt/go.mod`
- Modify: every `*.go` file under `wt/` that imports `github.com/ohanaverse/agent-worktree/...`

**Interfaces:**
- Consumes: `wt/` from Task 3.
- Produces: a `wt/` tree whose module path is `github.com/ohanaverse/local-ai-setup/wt`, building and testing clean. Tasks 7 and 9 (Go contract tests, wt-ci) assume this module path is already correct.

- [ ] **Step 1: Rename the module in `go.mod`**

```bash
cd wt
sed -i '' 's#^module github.com/ohanaverse/agent-worktree$#module github.com/ohanaverse/local-ai-setup/wt#' go.mod
head -3 go.mod
```

Expected: first line reads `module github.com/ohanaverse/local-ai-setup/wt`.

- [ ] **Step 2: Rewrite every internal import**

```bash
grep -rl 'github.com/ohanaverse/agent-worktree' --include="*.go" . | \
  xargs sed -i '' 's#github.com/ohanaverse/agent-worktree#github.com/ohanaverse/local-ai-setup/wt#g'
grep -rl 'github.com/ohanaverse/agent-worktree' --include="*.go" . || echo "no remaining references"
```

Expected: `no remaining references` printed.

- [ ] **Step 3: Build and test**

```bash
go build ./...
go vet ./...
go test ./...
```

Expected: all three pass with no errors. This is the module-rename equivalent of "run the tests and make sure they pass" — if `go test` fails on something unrelated to the rename (e.g. a codesign check), see `wt/CLAUDE.md`'s macOS codesign note before assuming the rename broke it.

- [ ] **Step 4: Commit**

```bash
cd ..
git add wt/
git commit -m "refactor(wt): rename Go module to github.com/ohanaverse/local-ai-setup/wt"
```

---

### Task 5: Fix path-sensitive docs and scripts

**Files:**
- Modify: `docs/guides/03-model-families.md`
- Modify: `docs/guides/06-wt-agents-and-models.md`
- Modify: `docs/guides/08-maintenance-and-troubleshooting.md`

**Interfaces:**
- Consumes: `wt/` existing at its new path (Task 3/4).
- Produces: guide docs whose rebuild command matches the new layout. `make check-links` (Task 10) verifies no link broke.

- [ ] **Step 1: Find every reference to the old rebuild command**

```bash
grep -rn 'go build -o.*\./cmd/wt' docs/guides/
```

Expected: hits in guides 03, 06, 08 per `issues.md` item 5 (the `~/.local/bin/wt` form, not the old ineffective GOPATH form which was already fixed).

- [ ] **Step 2: Update each to the new relative path**

Change `go build -o /Users/keith/.local/bin/wt ./cmd/wt` to `go build -o /Users/keith/.local/bin/wt ./wt/cmd/wt` in each of the three files found in Step 1 (use the Edit tool per file — the exact surrounding text differs per guide, so this is not a blind sed).

- [ ] **Step 3: Grep both merged trees for any other repo-root-relative assumption**

```bash
grep -rn 'MODELMAN_WT_DIR\|agent-wt' modelman/src wt/internal docs/guides | grep -v '~/.config'
```

Expected: no hits pointing at a source-tree path (the real ones are all `~/.config/agent-wt`-style runtime config dirs, which are unaffected by this migration). If something else turns up, fix it in this step.

- [ ] **Step 4: Verify links**

```bash
make check-links
```

Expected: passes with no broken links reported.

- [ ] **Step 5: Commit**

```bash
git add docs/guides/03-model-families.md docs/guides/06-wt-agents-and-models.md docs/guides/08-maintenance-and-troubleshooting.md
git commit -m "docs: update wt rebuild command for new monorepo path"
```

---

### Task 6: Create shared contract fixtures

**Files:**
- Create: `docs/contracts/registry.sample.toml`
- Create: `docs/contracts/usage.sample.jsonl`
- Create: `docs/contracts/rotation.sample.state`

**Interfaces:**
- Consumes: nothing (pure new fixture data).
- Produces: fixture files that Task 7 (Go tests) and Task 8 (Python tests) both load by relative path. The exact field names/values below are what those tasks assert against — if you change a value here, update both tasks' assertions to match.

- [ ] **Step 1: Write the registry fixture**

Create `docs/contracts/registry.sample.toml`:

```toml
# Shared fixture for cross-language contract tests. Exercises every schema
# variant modelman writes and wt reads: both provider auth types, all three
# Cost kinds, model_info, a per-model location override, and usage_tier.
#
# Read by:
#   - wt/internal/config/registry_fixture_test.go (Go)
#   - modelman/tests/contracts/test_registry_fixture.py (Python)
#
# The point of this file is that changing a field here without updating
# both tests should make both CI jobs fail in the same PR.

[[providers]]
id = "ollama"
name = "Ollama"
location = "local"
[providers.auth]
type = "none"
base_url = "http://localhost:11434"

[[providers]]
id = "openrouter"
name = "OpenRouter"
location = "cloud"
[providers.auth]
type = "api_key"
base_url = "https://openrouter.ai/api/v1"
secret_ref = "OPENROUTER_API_KEY"

[[families]]
name = "contract-fixture"
display_name = "Contract Fixture"

[[models]]
id = "ollama/contract-fixture:local"
family = "contract-fixture"
provider_id = "ollama"
model_name = "contract-fixture:local"
tags = ["code"]
[models.cost]
kind = "free"

[[models]]
id = "openrouter/contract-fixture:cloud"
family = "contract-fixture"
provider_id = "openrouter"
model_name = "org/contract-fixture-cloud"
location = "cloud"
tags = ["design"]
model_info = { supports_function_calling = true }
[models.cost]
kind = "per_token"
price_per_million_tokens = 1.5

[[models]]
id = "ollama/contract-fixture:subscription"
family = "contract-fixture"
provider_id = "ollama"
model_name = "contract-fixture:subscription"
tags = ["chat"]
usage_tier = "medium"
[models.cost]
kind = "subscription"
price_per_period = 20.0
period = "month"
```

- [ ] **Step 2: Write the usage fixture**

Create `docs/contracts/usage.sample.jsonl`:

```
{"model_id":"ollama/contract-fixture:local","timestamp":"2026-08-30T12:00:00Z"}
{"model_id":"ollama/contract-fixture:local","timestamp":"2026-08-20T08:00:00Z"}
{"model_id":"openrouter/contract-fixture:cloud","timestamp":"2026-07-01T00:00:00Z"}
```

- [ ] **Step 3: Write the rotation-state fixture**

Create `docs/contracts/rotation.sample.state`:

```
ollama/contract-fixture:local
```

- [ ] **Step 4: Commit**

```bash
git add docs/contracts/
git commit -m "test: add shared registry/usage contract fixtures"
```

---

### Task 7: Add Go contract tests

**Files:**
- Create: `wt/internal/config/registry_fixture_test.go`
- Create: `wt/internal/usage/usage_fixture_test.go`

**Interfaces:**
- Consumes: `docs/contracts/registry.sample.toml`, `docs/contracts/usage.sample.jsonl` (Task 6); the unexported `loadRegistry() ([]Provider, []Model, error)` in `wt/internal/config/registry.go` and the unexported `event struct { ModelID string; Timestamp time.Time }` in `wt/internal/usage/usage.go`.
- Produces: two new Go tests that fail if the fixture files change incompatibly. `wt-ci.yml` (Task 9) runs these.

- [ ] **Step 1: Write the failing registry contract test**

Create `wt/internal/config/registry_fixture_test.go`:

```go
package config

import "testing"

// TestLoadRegistryMatchesSharedFixture guards wt's registry.toml decoding
// against the schema modelman actually writes. The fixture at
// docs/contracts/registry.sample.toml is also read by modelman's
// tests/contracts/test_registry_fixture.py — if a schema change isn't
// reflected in both tests, both CI jobs fail in the same PR instead of
// drifting silently.
func TestLoadRegistryMatchesSharedFixture(t *testing.T) {
	t.Setenv("MODELMAN_REGISTRY", "../../../docs/contracts/registry.sample.toml")

	providers, models, err := loadRegistry()
	if err != nil {
		t.Fatalf("loadRegistry() error: %v", err)
	}

	if len(providers) != 2 {
		t.Fatalf("got %d providers, want 2", len(providers))
	}
	ollama, openrouter := providers[0], providers[1]
	if ollama.ID != "ollama" || ollama.Auth.Type != "none" || ollama.Auth.BaseURL != "http://localhost:11434" {
		t.Errorf("ollama provider decoded wrong: %+v", ollama)
	}
	if openrouter.ID != "openrouter" || openrouter.Auth.Type != "api_key" || openrouter.Auth.SecretRef != "OPENROUTER_API_KEY" {
		t.Errorf("openrouter provider decoded wrong: %+v", openrouter)
	}

	if len(models) != 3 {
		t.Fatalf("got %d models, want 3", len(models))
	}
	cloud := models[1]
	if cloud.ID != "openrouter/contract-fixture:cloud" || cloud.Location != "cloud" || cloud.ProviderID != "openrouter" {
		t.Errorf("cloud model decoded wrong: %+v", cloud)
	}
}
```

- [ ] **Step 2: Run it to verify it passes against the fixture**

```bash
cd wt
go test ./internal/config/ -run TestLoadRegistryMatchesSharedFixture -v
```

Expected: PASS. (This test can't meaningfully "fail first" the way TDD normally does — the fixture and the production decoder already both exist from Tasks 4 and 6. If it fails, the fixture or the assertions above have a mismatch with the real `Provider`/`Model` struct fields in `wt/internal/config/config.go` — fix the test, not the production code.)

- [ ] **Step 3: Write the failing usage contract test**

Create `wt/internal/usage/usage_fixture_test.go`:

```go
package usage

import (
	"bufio"
	"encoding/json"
	"os"
	"testing"
	"time"
)

// TestEventFieldsMatchSharedFixture guards the JSON contract between wt's
// usage.jsonl writer and modelman's reader (modelman/src/modelman/usage/wt_state.py).
// The fixture at docs/contracts/usage.sample.jsonl is read by both this test
// and modelman's tests/contracts/test_wt_state_fixture.py — if either side
// renames model_id/timestamp, both fail in the same PR.
func TestEventFieldsMatchSharedFixture(t *testing.T) {
	f, err := os.Open("../../../docs/contracts/usage.sample.jsonl")
	if err != nil {
		t.Fatalf("open fixture: %v", err)
	}
	defer f.Close()

	var events []event
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			continue
		}
		var e event
		if err := json.Unmarshal([]byte(line), &e); err != nil {
			t.Fatalf("unmarshal %q: %v", line, err)
		}
		events = append(events, e)
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("scan fixture: %v", err)
	}

	if len(events) != 3 {
		t.Fatalf("got %d events, want 3", len(events))
	}
	if events[0].ModelID != "ollama/contract-fixture:local" {
		t.Errorf("events[0].ModelID = %q, want %q", events[0].ModelID, "ollama/contract-fixture:local")
	}
	wantTS := time.Date(2026, 8, 30, 12, 0, 0, 0, time.UTC)
	if !events[0].Timestamp.Equal(wantTS) {
		t.Errorf("events[0].Timestamp = %v, want %v", events[0].Timestamp, wantTS)
	}
}
```

- [ ] **Step 4: Run both new tests**

```bash
go test ./internal/config/... ./internal/usage/... -v
```

Expected: both `TestLoadRegistryMatchesSharedFixture` and `TestEventFieldsMatchSharedFixture` PASS.

- [ ] **Step 5: Commit**

```bash
cd ..
git add wt/internal/config/registry_fixture_test.go wt/internal/usage/usage_fixture_test.go
git commit -m "test(wt): add contract tests against shared docs/contracts fixtures"
```

---

### Task 8: Add Python contract tests

**Files:**
- Create: `modelman/tests/contracts/__init__.py`
- Create: `modelman/tests/contracts/test_registry_fixture.py`
- Create: `modelman/tests/contracts/test_wt_state_fixture.py`

**Interfaces:**
- Consumes: `docs/contracts/*` (Task 6); `load_registry(path: Path | None = None) -> Registry` from `modelman/src/modelman/registry.py`; `read_usage_counts(path: Path, as_of: datetime) -> dict[str, LaunchCounts]` and `read_last_launched(path: Path) -> str | None` from `modelman/src/modelman/usage/wt_state.py`.
- Produces: two new Python test modules. `modelman-ci.yml` (Task 9) runs these.

- [ ] **Step 1: Create the test package init**

Create `modelman/tests/contracts/__init__.py` (empty file).

- [ ] **Step 2: Write the registry contract test**

Create `modelman/tests/contracts/test_registry_fixture.py`:

```python
from pathlib import Path

from modelman.registry import load_registry

FIXTURE = Path(__file__).resolve().parents[3] / "docs" / "contracts" / "registry.sample.toml"


def test_load_registry_matches_shared_fixture():
    """Guards modelman's registry.toml schema against wt's Go decoder
    (wt/internal/config/registry_fixture_test.go reads the same file). A
    schema change not reflected in both tests fails both CI jobs in the
    same PR instead of drifting silently.
    """
    registry = load_registry(path=FIXTURE)

    assert len(registry.providers) == 2
    ollama = registry.provider("ollama")
    assert ollama.auth.type == "none"
    assert ollama.auth.base_url == "http://localhost:11434"
    openrouter = registry.provider("openrouter")
    assert openrouter.auth.type == "api_key"
    assert openrouter.auth.secret_ref == "OPENROUTER_API_KEY"

    assert len(registry.models) == 3
    free_model = registry.model("ollama/contract-fixture:local")
    assert free_model.cost.kind == "free"

    cloud_model = registry.model("openrouter/contract-fixture:cloud")
    assert cloud_model.location == "cloud"
    assert cloud_model.cost.kind == "per_token"
    assert cloud_model.cost.price_per_million_tokens == 1.5
    assert cloud_model.model_info == {"supports_function_calling": True}

    sub_model = registry.model("ollama/contract-fixture:subscription")
    assert sub_model.cost.kind == "subscription"
    assert sub_model.cost.price_per_period == 20.0
    assert sub_model.cost.period == "month"
    assert sub_model.usage_tier == "medium"

    family = registry.family("contract-fixture")
    assert family is not None
    assert family.display_name == "Contract Fixture"
```

- [ ] **Step 3: Run it**

```bash
cd modelman
uv run pytest tests/contracts/test_registry_fixture.py -v
```

Expected: PASS. As with the Go side (Task 7), this exercises an already-existing fixture and decoder — if it fails, fix the test's assertions against the real `Registry`/`ModelEntry`/`Cost` dataclasses in `src/modelman/registry.py`, not the production code.

- [ ] **Step 4: Write the usage/rotation contract test**

Create `modelman/tests/contracts/test_wt_state_fixture.py`:

```python
from datetime import UTC, datetime
from pathlib import Path

from modelman.usage.wt_state import read_last_launched, read_usage_counts

FIXTURES = Path(__file__).resolve().parents[3] / "docs" / "contracts"


def test_read_usage_counts_matches_fixture():
    """Guards modelman's usage.jsonl reader against wt's writer
    (wt/internal/usage/usage_fixture_test.go reads the same fixture file).
    Exercises the 1d/7d/30d window boundaries and the "omit if outside the
    largest window" behavior, which a naive schema change could silently
    break without any test noticing.
    """
    as_of = datetime(2026, 8, 31, tzinfo=UTC)
    counts = read_usage_counts(FIXTURES / "usage.sample.jsonl", as_of)

    local = counts["ollama/contract-fixture:local"]
    assert (local._1d, local._7d, local._30d) == (1, 1, 2)
    assert "openrouter/contract-fixture:cloud" not in counts


def test_read_last_launched_matches_fixture():
    """Guards the single-line rotation.state format wt writes and modelman
    reads to report the last-launched model."""
    assert read_last_launched(FIXTURES / "rotation.sample.state") == "ollama/contract-fixture:local"
```

- [ ] **Step 5: Run both new test files**

```bash
uv run pytest tests/contracts/ -v
```

Expected: all 3 tests (`test_load_registry_matches_shared_fixture`, `test_read_usage_counts_matches_fixture`, `test_read_last_launched_matches_fixture`) PASS.

- [ ] **Step 6: Commit**

```bash
cd ..
git add modelman/tests/contracts/
git commit -m "test(modelman): add contract tests against shared docs/contracts fixtures"
```

---

### Task 9: Add CI workflows

**Files:**
- Create: `.github/workflows/modelman-ci.yml`
- Create: `.github/workflows/wt-ci.yml`
- Create: `.github/workflows/shell-ci.yml`
- Modify: `Makefile` (root)

**Interfaces:**
- Consumes: `modelman/Makefile`'s `check`/`test` targets, `wt/`'s `go build`/`go vet`/`go test`, root `Makefile`'s `lint` target — all pre-existing.
- Produces: three CI workflows. Task 12's smoke test verifies `docs/contracts/**` changes trigger both `modelman-ci` and `wt-ci`.

- [ ] **Step 1: Write `modelman-ci.yml`**

Create `.github/workflows/modelman-ci.yml`:

```yaml
name: modelman-ci

on:
  push:
    branches: [main]
    paths:
      - "modelman/**"
      - "docs/contracts/**"
  pull_request:
    paths:
      - "modelman/**"
      - "docs/contracts/**"

jobs:
  test:
    runs-on: ubuntu-latest
    defaults:
      run:
        working-directory: modelman
    steps:
      - uses: actions/checkout@v4
      - uses: astral-sh/setup-uv@v3
      - run: uv sync
      - run: make check
      - run: make test
```

- [ ] **Step 2: Write `wt-ci.yml`**

Create `.github/workflows/wt-ci.yml`:

```yaml
name: wt-ci

on:
  push:
    branches: [main]
    paths:
      - "wt/**"
      - "docs/contracts/**"
  pull_request:
    paths:
      - "wt/**"
      - "docs/contracts/**"

jobs:
  test:
    runs-on: macos-latest
    defaults:
      run:
        working-directory: wt
    steps:
      - uses: actions/checkout@v4
      - uses: actions/setup-go@v5
        with:
          go-version: "1.26.7"
      - run: go build ./...
      - run: go vet ./...
      - run: go test ./...
```

- [ ] **Step 3: Write `shell-ci.yml`**

Create `.github/workflows/shell-ci.yml`:

```yaml
name: shell-ci

on:
  push:
    branches: [main]
    paths:
      - "bin/**"
      - "benchmarks/**"
      - "docs/**"
      - "Makefile"
  pull_request:
    paths:
      - "bin/**"
      - "benchmarks/**"
      - "docs/**"
      - "Makefile"

jobs:
  lint:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - run: make lint
```

- [ ] **Step 4: Add aggregate `test-all` target to the root Makefile**

Read the current root `Makefile` first, then add a target like:

```makefile
.PHONY: test-all
test-all: lint
	cd modelman && uv sync && make check && make test
	cd wt && go build ./... && go vet ./... && go test ./...
```

(Match the existing Makefile's style for `.PHONY` declarations and target ordering — read the file before editing so this fits in rather than being appended blindly.)

- [ ] **Step 5: Run the new aggregate target locally**

```bash
make test-all
```

Expected: passes end-to-end (root lint, modelman check+test, wt build+vet+test).

- [ ] **Step 6: Commit**

```bash
git add .github/workflows/ Makefile
git commit -m "ci: add path-filtered modelman/wt/shell CI workflows and test-all target"
```

---

### Task 10: Full-repo verification pass

**Files:** none created or modified — this task only runs verification commands from the spec's Testing section.

**Interfaces:**
- Consumes: everything from Tasks 2–9.
- Produces: confidence that the merge is complete and correct before the smoke test (Task 11) and the external-visible steps (Tasks 12–13).

- [ ] **Step 1: Root lint**

```bash
make lint
```

Expected: passes (shellcheck + check-links).

- [ ] **Step 2: wt build/vet/test**

```bash
cd wt && go build ./... && go vet ./... && go test ./... && cd ..
```

Expected: all pass.

- [ ] **Step 3: modelman check/test**

```bash
cd modelman && uv sync && make check && make test && cd ..
```

Expected: all pass.

- [ ] **Step 4: Prove the contract tests actually read the shared file (not a stale copy)**

```bash
cp docs/contracts/registry.sample.toml /tmp/registry.sample.toml.bak
sed -i '' 's/type = "none"/type = "BROKEN"/' docs/contracts/registry.sample.toml
(cd wt && go test ./internal/config/ -run TestLoadRegistryMatchesSharedFixture -v) ; echo "wt exit: $?"
(cd modelman && uv run pytest tests/contracts/test_registry_fixture.py -v) ; echo "modelman exit: $?"
mv /tmp/registry.sample.toml.bak docs/contracts/registry.sample.toml
```

Expected: both `wt exit` and `modelman exit` are non-zero (both tests fail on the corrupted fixture), proving the fixture is load-bearing for both. Then confirm the restore worked:

```bash
git status docs/contracts/registry.sample.toml
```

Expected: no diff (file restored to the committed version).

No commit — this task is pure verification, and Step 4 restores the file it perturbed.

---

### Task 11: End-to-end smoke test

**Files:** none created or modified.

**Interfaces:**
- Consumes: the rebuilt `wt` binary from `wt/cmd/wt` (Task 4/5's path), the `modelman` TUI entry point.
- Produces: confirmation that runtime behavior (not just tests) still works after the migration.

- [ ] **Step 1: Rebuild `wt` from its new path and confirm it runs**

```bash
go build -o ~/.local/bin/wt ./wt/cmd/wt
wt --version
```

Expected: prints a version string, matching what `wt --version` printed before the migration (no version bump implied by this migration).

- [ ] **Step 2: Confirm a real `claude-wt` launch still works**

```bash
claude-wt --cwd --check-guard
```

Expected: reports the main-guard status without error (this repo's `block-main-commit` hook, seen in the earlier commit attempt, confirms `wt`'s guard machinery still works post-move).

- [ ] **Step 3: Confirm the modelman TUI still launches**

```bash
cd modelman && timeout 3 uv run modelman --help; echo "exit: $?"; cd ..
```

Expected: prints the Typer CLI help text and exits 0 (a `--help` invocation exits immediately rather than opening the interactive TUI, so this is safe to run headless).

- [ ] **Step 4: Run the guides' 60-second health check block unchanged**

```bash
curl -s -m 2 http://localhost:4000/v1/models -o /dev/null -w "4000(litellm):%{http_code}\n"
curl -s -m 2 http://localhost:8080/health -o /dev/null -w "8080(llama.cpp):%{http_code}\n"
curl -s -m 2 http://localhost:8000/health -o /dev/null -w "8000(omlx):%{http_code}\n"
curl -s -m 2 http://localhost:11434/api/tags -o /dev/null -w "11434(ollama):%{http_code}\n"
```

Expected (per `README.md`'s existing health-check table): `401` on `:4000`, `200` on the other three. This confirms the migration didn't touch anything the running LiteLLM/backend stack depends on (it shouldn't have — nothing in this plan touches `~/.config` or the LaunchAgents — but this is the cheap way to be sure).

No commit — pure verification.

---

### Task 12: Push and confirm CI triggers (requires explicit go-ahead before running)

**STOP before this task: confirm with the user that they want to push the branch and open a PR now.** This is the first action in this plan visible outside the local machine.

**Files:** none created or modified beyond what's already committed in Tasks 2–9.

**Interfaces:**
- Consumes: the branch built up by Tasks 2–9.
- Produces: a merged (or at least CI-verified) branch in `local-ai-setup` on GitHub.

- [ ] **Step 1: Get explicit user confirmation to push**

Ask: "Ready to push `<branch-name>` to `origin` and open a PR against `local-ai-setup`?" Do not proceed past this step without an explicit yes.

- [ ] **Step 2: Push the branch**

```bash
git push -u origin <branch-name>
```

- [ ] **Step 3: Open the PR**

```bash
gh pr create --repo ohanaverse/local-ai-setup --title "Consolidate modelman and agent-worktree into this repo" --body "$(cat <<'EOF'
## Summary
- Merges modelman/ and wt/ (formerly agent-worktree) into this repo via git subtree, preserving history
- Renames the wt Go module path to github.com/ohanaverse/local-ai-setup/wt
- Adds path-filtered CI (modelman-ci, wt-ci, shell-ci) and shared docs/contracts/ fixtures so a schema change to registry.toml or usage.jsonl fails both languages' tests in the same PR

## Test plan
- [x] wt: go build/vet/test pass
- [x] modelman: make check/test pass
- [x] root: make lint passes
- [x] contract tests fail together when the shared fixture is corrupted (verified, then reverted)
- [x] wt/modelman/health-check smoke tests pass

Spec: docs/superpowers/specs/2026-08-31-three-repo-monorepo-consolidation-design.md
Plan: docs/superpowers/plans/2026-08-31-three-repo-monorepo-consolidation.md
EOF
)"
```

- [ ] **Step 4: Confirm both CI jobs actually ran**

```bash
gh pr checks --repo ohanaverse/local-ai-setup <pr-number>
```

Expected: `modelman-ci`, `wt-ci`, and `shell-ci` all present and passing. Note this doesn't yet prove the `docs/contracts/**` shared trigger works in isolation — this PR touches `modelman/**` and `wt/**` directly too, so of course both ran. Step 5 isolates that proof.

- [ ] **Step 5: After this PR merges, isolate the shared-trigger proof with a fixture-only PR**

```bash
git checkout main && git pull
git checkout -b docs/contracts-trigger-check
sed -i '' 's/type = "none"/type = "none"  # trigger-check/' docs/contracts/registry.sample.toml
git add docs/contracts/registry.sample.toml
git commit -m "test: verify docs/contracts change triggers both CI jobs"
git push -u origin docs/contracts-trigger-check
gh pr create --repo ohanaverse/local-ai-setup --title "Verify shared contract-fixture CI trigger" --body "Touches only docs/contracts/registry.sample.toml — confirms modelman-ci and wt-ci both trigger on a fixture-only change, proving the cross-language safety net this migration exists for."
gh pr checks --repo ohanaverse/local-ai-setup <new-pr-number>
```

Expected: both `modelman-ci` and `wt-ci` trigger and pass on a PR that touches nothing under `modelman/` or `wt/` — this is the actual proof the mechanism works. Then close this PR without merging (it's a verification-only change) or merge it — either is fine, it's a harmless comment-only edit.

---

### Task 13: Archive the source repos (requires explicit go-ahead before running)

**STOP before this task: this is irreversible-in-spirit (archived repos can be unarchived, but it's a real, externally-visible state change) and must not run without the user explicitly saying to proceed, done immediately before running, not assumed from earlier approval of the plan.**

**Files:**
- Modify: `README.md` in the (about-to-be-archived) `modelman` repo
- Modify: `README.md` in the (about-to-be-archived) `agent-worktree` repo

**Interfaces:**
- Consumes: a merged and verified `local-ai-setup` (Tasks 1–12 complete, PR merged).
- Produces: two read-only, pointer-README'd GitHub repos.

- [ ] **Step 1: Get explicit user confirmation, naming both repos being archived**

Ask: "Ready to archive `ohanaverse/modelman` and `ohanaverse/agent-worktree` on GitHub? They'll become read-only; not deleted." Do not proceed without an explicit yes.

- [ ] **Step 2: Replace each repo's README with a pointer**

In a local clone of each (not inside `local-ai-setup`), replace `README.md` with:

```markdown
# modelman

Moved into [ohanaverse/local-ai-setup](https://github.com/ohanaverse/local-ai-setup)
as the `modelman/` subdirectory. This repo is archived (read-only); history
is preserved there via `git subtree`.
```

(and the equivalent for `agent-worktree`, referencing the `wt/` subdirectory). Commit and push each on its own `main` branch before archiving — an archived repo can still be pushed to only after being unarchived, so this must happen first.

- [ ] **Step 3: Archive both repos**

```bash
gh repo archive ohanaverse/modelman --yes
gh repo archive ohanaverse/agent-worktree --yes
```

- [ ] **Step 4: Verify**

```bash
gh repo view ohanaverse/modelman --json isArchived
gh repo view ohanaverse/agent-worktree --json isArchived
```

Expected: both report `"isArchived": true`.

No further commit — this task's commits happened in Step 2, in the source repos, not in `local-ai-setup`.
