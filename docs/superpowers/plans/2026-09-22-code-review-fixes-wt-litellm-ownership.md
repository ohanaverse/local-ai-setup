# Code Review Fixes (wt-litellm-ownership) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Fix the 10 confirmed findings from the `/code-review` run on branch `feat/wt-litellm-modelman-delegation` (`git diff main...HEAD`), covering `wt/internal/litellm`, `wt/internal/lifecycle`, and `modelman/src/modelman`.

**Architecture:** Each finding gets its own task with a regression test written first (or alongside, where a pure refactor has no new observable behavior to assert beforehand) then the fix. Go fixes land first (grouped by file: `entry.go` → `service.go` → `configfile.go` → `lifecycle/routes.go`), then Python fixes (`wt_bridge.py` → `litellm.py` → `screens/models.py`), so each task's diff stays scoped to one file family and doesn't require rebasing across languages.

**Tech Stack:** Go 1.26 (`wt/`), Python 3.13 + `uv`/pytest/Textual (`modelman/`).

**Spec:** No separate spec doc — the "spec" is the 10 findings from the `/code-review` run, reproduced per-task below with their file:line, failure scenario, and the exact code to change.

## Global Constraints

- Run `go build ./... && go vet ./... && go test ./...` from `wt/` after every Go task.
- Run `make check` (lint + `ruff format --check` + typecheck) and the relevant focused `pytest` files after every Python task; run `make all` once at the end (per `modelman/CLAUDE.md`'s testing guidance).
- Every new Go test has a `//` comment stating what it tests and why (per `wt/CLAUDE.md`'s Go test convention). Every new Python test has a comment describing the scenario and why it matters (per the user's global Test Documentation preference).
- One git commit per task, referencing the review finding it fixes (e.g. `fix(wt): recover instead of panicking on unencodable model_info - completes plan item #1`).
- Do not touch the CONFIRMED-but-cut findings (duplicate atomic-write/flock code, duplicate HTTP-poll loop, the two CLAUDE.md process findings) — out of scope for this plan.

---

## Task 1: `entry.go` — return an error instead of panicking on bad `model_info`

**Review finding:** `wt/internal/litellm/entry.go:36` — `toNode()` panics (unrecovered) when a registry model's `cost`/`model_info` value can't be yaml-encoded. Failure scenario: a `registry.toml` with a `model_info` value shape `yaml.Node.Encode` rejects crashes the whole `wt` process on `expose`/`unexpose`/`sync`/`start` instead of surfacing a per-model error.

**Files:**
- Modify: `wt/internal/litellm/entry.go`
- Modify: `wt/internal/litellm/configfile.go` (two call sites of `toNode`/`mapping` whose signatures change)
- Test: `wt/internal/litellm/entry_test.go`

**Interfaces:**
- `toNode(v any) (*yaml.Node, error)` (was `*yaml.Node`)
- `mapping(pairs []kv) (*yaml.Node, error)` (was `*yaml.Node`)
- `BuildEntry` keeps its existing signature `(m config.Model, p config.Provider) (*yaml.Node, error)` — it already returns an error, so `prepare()`/`Apply`/`Check` in `service.go` need no changes.

- [ ] **Step 1: Write the failing test**

Add to `wt/internal/litellm/entry_test.go`:

```go
// TestBuildEntryUnencodableModelInfoReturnsError pins that a model_info value
// yaml.Node.Encode cannot marshal (a hand-edited registry.toml decoded into a
// weird shape) surfaces as a per-model error from BuildEntry instead of
// panicking and crashing the whole wt process on expose/unexpose/sync/start.
func TestBuildEntryUnencodableModelInfoReturnsError(t *testing.T) {
	cfg := testConfig()
	m := cfg.Models[2] // openrouter/x/y
	m.ModelInfo = map[string]any{"bad": make(chan int)}
	if _, err := BuildEntry(m, cfg.Providers[2]); err == nil {
		t.Fatal("want an error, got nil (and no panic)")
	}
}
```

- [ ] **Step 2: Run test to verify it fails (panics)**

Run: `cd wt && go test ./internal/litellm -run TestBuildEntryUnencodableModelInfoReturnsError -v`
Expected: the test process panics/crashes rather than reporting a clean `FAIL`.

- [ ] **Step 3: Change `toNode`/`mapping` to return errors**

In `wt/internal/litellm/entry.go`, replace:

```go
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
```

with:

```go
// mapping builds an ordered YAML mapping node.
func mapping(pairs []kv) (*yaml.Node, error) {
	n := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
	for _, p := range pairs {
		val, err := toNode(p.val)
		if err != nil {
			return nil, fmt.Errorf("key %q: %w", p.key, err)
		}
		n.Content = append(n.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: p.key}, val)
	}
	return n, nil
}

// toNode encodes v as a YAML node. Registry values are TOML scalars/arrays/
// tables and always encode, but a hand-edited registry.toml can decode into a
// shape yaml.Node.Encode rejects — that must surface as a per-model error
// (BuildEntry already returns one), never a process-crashing panic.
func toNode(v any) (*yaml.Node, error) {
	if n, ok := v.(*yaml.Node); ok {
		return n, nil
	}
	n := &yaml.Node{}
	if err := n.Encode(v); err != nil {
		return nil, fmt.Errorf("cannot encode %T: %w", v, err)
	}
	return n, nil
}
```

Then update `BuildEntry` (same file) — replace:

```go
	return mapping([]kv{
		{"model_name", m.ID},
		{"litellm_params", mapping(params)},
		{"model_info", mapping(info)},
	}), nil
}
```

with:

```go
	paramsNode, err := mapping(params)
	if err != nil {
		return nil, fmt.Errorf("model %q: %w", m.ID, err)
	}
	infoNode, err := mapping(info)
	if err != nil {
		return nil, fmt.Errorf("model %q: %w", m.ID, err)
	}
	return mapping([]kv{
		{"model_name", m.ID},
		{"litellm_params", paramsNode},
		{"model_info", infoNode},
	})
}
```

(the outer `mapping([]kv{...})` call now also returns `(*yaml.Node, error)`, which is exactly `BuildEntry`'s own return shape, so `return mapping(...)` works directly — `paramsNode`/`infoNode` are already `*yaml.Node` and `toNode`'s `v.(*yaml.Node)` fast path returns them unchanged.)

- [ ] **Step 4: Fix the two call sites in `configfile.go`**

In `wt/internal/litellm/configfile.go`, replace:

```go
func boolNode(v bool) *yaml.Node { return toNode(v) }
```

with:

```go
// boolNode encodes a literal bool, which always succeeds.
func boolNode(v bool) *yaml.Node {
	n, _ := toNode(v)
	return n
}
```

And in `EnsureSettings`, replace:

```go
	ls := mapGet(root, "litellm_settings")
	if ls == nil || isNull(ls) {
		ls = mapping(nil)
		mapSet(root, "litellm_settings", ls)
	}
```

with:

```go
	ls := mapGet(root, "litellm_settings")
	if ls == nil || isNull(ls) {
		ls, _ = mapping(nil) // nil pairs never fail
		mapSet(root, "litellm_settings", ls)
	}
```

And replace:

```go
		case strings.HasPrefix(model.Value, "ollama_chat/") && mapGet(params, "additional_drop_params") == nil:
			seq := &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq", Content: []*yaml.Node{toNode("reasoning_effort")}}
			mapSet(params, "additional_drop_params", seq)
```

with:

```go
		case strings.HasPrefix(model.Value, "ollama_chat/") && mapGet(params, "additional_drop_params") == nil:
			reasoningNode, _ := toNode("reasoning_effort") // literal string always succeeds
			seq := &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq", Content: []*yaml.Node{reasoningNode}}
			mapSet(params, "additional_drop_params", seq)
```

- [ ] **Step 5: Run the failing test again, then the whole package**

Run: `cd wt && go build ./... && go test ./internal/litellm/... -v`
Expected: `TestBuildEntryUnencodableModelInfoReturnsError` PASSes (no panic, error returned), and every other `internal/litellm` test still passes unchanged.

- [ ] **Step 6: Commit**

```bash
git add wt/internal/litellm/entry.go wt/internal/litellm/configfile.go wt/internal/litellm/entry_test.go
git commit -m "$(cat <<'EOF'
fix(wt): return an error instead of panicking on unencodable model_info

toNode/mapping now return errors instead of panicking; BuildEntry already
propagates a per-model error, so a bad registry.toml value no longer
crashes the whole wt process on expose/unexpose/sync/start.

completes plan item #1
EOF
)"
```

---

## Task 2: `service.go` — `Sync` must recheck the `add` set too, not just `remove`

**Review finding:** `wt/internal/litellm/service.go:230` — `Sync()`'s plan closure re-verifies the `remove` set under the lock via `Options.Recheck`, but never re-verifies `add`. Failure scenario: `wt litellm sync` races a model stopping right after the outer liveness probe — the stale route is written into `config.yaml`, routing LiteLLM to a dead backend until the next sync/start/stop repairs it.

**Files:**
- Modify: `wt/internal/litellm/service.go`
- Test: `wt/internal/litellm/service_test.go`

**Interfaces:**
- `Sync(cfg *config.Config, running []string, o Options) (Result, error)` — signature unchanged.
- Reuses the existing `Options.Recheck func() []string` field — no signature change.

- [ ] **Step 1: Write the failing test**

Add to `wt/internal/litellm/service_test.go`, right after `TestSyncRecheckKeepsModelsThatStartedMeanwhile`:

```go
// TestSyncRecheckAlsoAppliesToAddSet pins the other half of the under-lock
// re-probe: a model the outer probe saw running but that stopped before the
// lock was acquired must not get a fresh route added for a backend that is
// no longer there. Only Recheck's remove-side filtering was covered before;
// this pins that add is filtered too.
func TestSyncRecheckAlsoAppliesToAddSet(t *testing.T) {
	o, _, p := opts(t, "model_list: []\n")
	// Outer probe says both are running; Recheck (under the lock) finds only
	// one still running.
	o.Recheck = func() []string { return []string{"ollama/gemma:9b"} }
	if _, err := Sync(testConfig(), []string{"ollama/gemma:9b", "mtplx/Youssofal--Q"}, o); err != nil {
		t.Fatal(err)
	}
	f, _ := Open(p)
	if got := strings.Join(f.RoutedIDs(), ","); got != "ollama/gemma:9b" {
		t.Fatalf("routed = %s, want only the model Recheck still found running", got)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd wt && go test ./internal/litellm -run TestSyncRecheckAlsoAppliesToAddSet -v`
Expected: FAIL — `routed = ollama/gemma:9b,mtplx/Youssofal--Q` (both got added; the second should have been filtered out).

- [ ] **Step 3: Fix `Sync`'s plan closure**

In `wt/internal/litellm/service.go`, replace:

```go
func Sync(cfg *config.Config, running []string, o Options) (Result, error) {
	o.SkipReadyGate = true
	return applyPlanned(cfg, func(f *File) (add, remove []string) {
		routed := f.RoutedIDs()
		for _, m := range LocalModels(cfg) {
			switch {
			case slices.Contains(o.Untouched, m.ID):
			case slices.Contains(running, m.ID):
				add = append(add, m.ID)
			case slices.Contains(routed, m.ID):
				remove = append(remove, m.ID)
			}
		}
		if len(remove) > 0 && o.Recheck != nil {
			fresh := o.Recheck()
			remove = slices.DeleteFunc(remove, func(id string) bool { return slices.Contains(fresh, id) })
		}
		return add, remove
	}, o)
}
```

with:

```go
func Sync(cfg *config.Config, running []string, o Options) (Result, error) {
	o.SkipReadyGate = true
	return applyPlanned(cfg, func(f *File) (add, remove []string) {
		routed := f.RoutedIDs()
		for _, m := range LocalModels(cfg) {
			switch {
			case slices.Contains(o.Untouched, m.ID):
			case slices.Contains(running, m.ID):
				add = append(add, m.ID)
			case slices.Contains(routed, m.ID):
				remove = append(remove, m.ID)
			}
		}
		// Recheck runs under the lock, right before add/remove are applied:
		// a model the outer probe saw stopped but that started meanwhile
		// must keep its route (remove-side), and one the outer probe saw
		// running but that stopped meanwhile must not get a fresh route for
		// a dead backend (add-side). Both sides are re-verified together so
		// one live probe settles the whole plan.
		if o.Recheck != nil && (len(add) > 0 || len(remove) > 0) {
			fresh := o.Recheck()
			if len(remove) > 0 {
				remove = slices.DeleteFunc(remove, func(id string) bool { return slices.Contains(fresh, id) })
			}
			if len(add) > 0 {
				add = slices.DeleteFunc(add, func(id string) bool { return !slices.Contains(fresh, id) })
			}
		}
		return add, remove
	}, o)
}
```

- [ ] **Step 4: Run the failing test again, then the whole package**

Run: `cd wt && go build ./... && go test ./internal/litellm/... -v`
Expected: `TestSyncRecheckAlsoAppliesToAddSet` PASSes, and `TestSyncRecheckKeepsModelsThatStartedMeanwhile` (the remove-side test) still PASSes unchanged.

- [ ] **Step 5: Commit**

```bash
git add wt/internal/litellm/service.go wt/internal/litellm/service_test.go
git commit -m "$(cat <<'EOF'
fix(wt): Sync rechecks the add set under the lock, not just remove

A model the outer probe saw running but that stopped before the
config.yaml lock was acquired no longer gets a stale route added —
Recheck's fresh result now filters both add and remove.

completes plan item #2
EOF
)"
```

---

## Task 3: `service.go`/`litellm.go` — make `--dry-run` output distinguishable from a real apply

**Review finding:** `wt/internal/litellm/service.go:111` — `Check()` (the `--dry-run` path) sets `Action="exposed"` identically to a real successful apply, so `--dry-run --json` output is indistinguishable from an actual write. Failure scenario: a script using `wt litellm expose --dry-run --json` to preview a change sees `{"action":"exposed"}` with no signal it was a no-op.

**Files:**
- Modify: `wt/cmd/wt/litellm.go` (`litellmResultJSON`, `reportLitellm`, its two call sites)
- Test: `wt/cmd/wt/litellm_test.go`

**Interfaces:**
- `reportLitellm(out, errOut io.Writer, res litellm.Result, asJSON, dryRun bool) error` (was `(out, errOut, res, asJSON) error`)

- [ ] **Step 1: Write the failing test**

Add to `wt/cmd/wt/litellm_test.go`, near `TestLitellmExposeGateAndDryRun`:

```go
// TestLitellmDryRunJSONIsDistinguishableFromRealApply pins that --dry-run
// --json output carries a "dry_run" marker: without it, a script previewing
// a change with --dry-run cannot tell the output apart from a real apply
// that happened to change nothing (both show changed:false, action:exposed).
func TestLitellmDryRunJSONIsDistinguishableFromRealApply(t *testing.T) {
	var out, errOut bytes.Buffer
	err := runLitellmChange(&out, &errOut, litellmTestConfig(), true, []string{"ollama/gemma:9b"}, litellmFlags{JSON: true, DryRun: true, SkipReadyGate: true})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), `"dry_run":true`) {
		t.Fatalf("dry-run JSON = %s, want a dry_run:true marker", out.String())
	}

	out.Reset()
	errOut.Reset()
	if err := runLitellmChange(&out, &errOut, litellmTestConfig(), true, []string{"ollama/gemma:9b"}, litellmFlags{JSON: true, SkipReadyGate: true}); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String(), `"dry_run"`) {
		t.Fatalf("real-apply JSON = %s, want no dry_run key at all", out.String())
	}
}
```

(Check the file's existing imports/helpers — `litellmTestConfig()` and `bytes`/`strings` should already be used elsewhere in this file; add imports only if genuinely missing.)

- [ ] **Step 2: Run test to verify it fails**

Run: `cd wt && go test ./cmd/wt -run TestLitellmDryRunJSONIsDistinguishableFromRealApply -v`
Expected: FAIL — no `"dry_run"` key present in the dry-run output.

- [ ] **Step 3: Add the `dry_run` field and thread it through**

In `wt/cmd/wt/litellm.go`, replace:

```go
type litellmResultJSON struct {
	Outcomes []litellmOutcomeJSON `json:"outcomes"`
	Changed  bool                 `json:"changed"`
	Warnings []string             `json:"warnings"`
}
```

with:

```go
type litellmResultJSON struct {
	Outcomes []litellmOutcomeJSON `json:"outcomes"`
	Changed  bool                 `json:"changed"`
	Warnings []string             `json:"warnings"`
	DryRun   bool                 `json:"dry_run,omitempty"`
}
```

Replace:

```go
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
```

with:

```go
func reportLitellm(out, errOut io.Writer, res litellm.Result, asJSON, dryRun bool) error {
	failed := false
	doc := litellmResultJSON{Outcomes: []litellmOutcomeJSON{}, Changed: res.Changed, Warnings: append([]string{}, res.Warnings...), DryRun: dryRun}
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
			switch {
			case j.Error != "":
				fmt.Fprintf(errOut, "%s: %s\n", j.ID, j.Error)
			case dryRun:
				fmt.Fprintf(out, "%s: would %s\n", j.ID, j.Action)
			default:
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
```

Then update its two call sites. In `runLitellmChange`, replace:

```go
	return reportLitellm(out, errOut, res, fl.JSON)
```

with:

```go
	return reportLitellm(out, errOut, res, fl.JSON, expose && fl.DryRun)
```

In `runLitellmSync`, replace:

```go
	return reportLitellm(out, errOut, res, asJSON)
```

with:

```go
	return reportLitellm(out, errOut, res, asJSON, false)
```

- [ ] **Step 4: Run the failing test again, then the whole package**

Run: `cd wt && go build ./... && go test ./cmd/wt/... -v`
Expected: `TestLitellmDryRunJSONIsDistinguishableFromRealApply` PASSes; every other `cmd/wt` test still passes (the new field is `omitempty`, so existing JSON-shape assertions on non-dry-run output are unaffected).

- [ ] **Step 5: Commit**

```bash
git add wt/cmd/wt/litellm.go wt/cmd/wt/litellm_test.go
git commit -m "$(cat <<'EOF'
fix(wt): mark --dry-run --json output with a dry_run field

expose --dry-run --json previously produced output identical to a real
apply that changed nothing; a script can now tell them apart.

completes plan item #3
EOF
)"
```

---

## Task 4: `configfile.go` — `isLoopback` must handle schemeless `api_base`

**Review finding:** `wt/internal/litellm/configfile.go:257` — `isLoopback()` parses `api_base` with `net/url.Parse` without checking for a scheme first, so a schemeless value like `"localhost:11434"` parses as an opaque URI (`Scheme="localhost"`, `Host=""`) and never matches `loopbackHosts`. Failure scenario: `migrate.go`'s legacy importer passes a schemeless `PROVIDER_OLLAMA_BASE_URL` straight into `api_base`; `EnsureSettings`'s `use_chat_completions_api` fix-up silently never applies for that row, leaving Responses-API-driven agents (codex) to 404/cooldown-blacklist.

**Files:**
- Modify: `wt/internal/litellm/configfile.go`
- Test: `wt/internal/litellm/configfile_test.go`

**Interfaces:** `isLoopback(n *yaml.Node) bool` — signature unchanged.

- [ ] **Step 1: Write the failing test**

Add to `wt/internal/litellm/configfile_test.go`, near `TestIsLoopbackCaseInsensitive`:

```go
// TestIsLoopbackHandlesSchemelessHost pins that a schemeless api_base
// ("localhost:11434", the shape migrate.go's legacy importer can produce)
// is recognized as loopback. url.Parse alone treats "localhost:11434" as an
// opaque scheme:opaque pair (Host==""), which silently skipped the
// use_chat_completions_api fix-up for that row.
func TestIsLoopbackHandlesSchemelessHost(t *testing.T) {
	cases := []struct {
		value string
		want  bool
	}{
		{"localhost:11434", true},
		{"127.0.0.1:8000", true},
		{"localhost", true},
		{"http://localhost:11434", true},
		{"example.com:1234", false},
		{"http://example.com", false},
	}
	for _, c := range cases {
		n := &yaml.Node{Kind: yaml.ScalarNode, Value: c.value}
		if got := isLoopback(n); got != c.want {
			t.Errorf("isLoopback(%q) = %v, want %v", c.value, got, c.want)
		}
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd wt && go test ./internal/litellm -run TestIsLoopbackHandlesSchemelessHost -v`
Expected: FAIL on the `"localhost:11434"` and `"localhost"` cases (both currently read as `false`).

- [ ] **Step 3: Fix `isLoopback`**

Replace:

```go
func isLoopback(n *yaml.Node) bool {
	if n == nil || n.Kind != yaml.ScalarNode {
		return false
	}
	u, err := url.Parse(n.Value)
	return err == nil && loopbackHosts[strings.ToLower(u.Hostname())]
}
```

with:

```go
func isLoopback(n *yaml.Node) bool {
	if n == nil || n.Kind != yaml.ScalarNode {
		return false
	}
	u, err := url.Parse(n.Value)
	if err != nil {
		return false
	}
	if u.Host == "" {
		// Schemeless input ("localhost:11434"): url.Parse reads the part
		// before the colon as a scheme and everything after as opaque, so
		// Host/Hostname() come back empty. Reparse with a "//" prefix so it
		// resolves as a host instead.
		u, err = url.Parse("//" + n.Value)
		if err != nil {
			return false
		}
	}
	return loopbackHosts[strings.ToLower(u.Hostname())]
}
```

- [ ] **Step 4: Run the failing test again, then the whole package**

Run: `cd wt && go build ./... && go test ./internal/litellm/... -v`
Expected: `TestIsLoopbackHandlesSchemelessHost` and `TestIsLoopbackCaseInsensitive` both PASS.

- [ ] **Step 5: Commit**

```bash
git add wt/internal/litellm/configfile.go wt/internal/litellm/configfile_test.go
git commit -m "$(cat <<'EOF'
fix(wt): isLoopback recognizes schemeless api_base values

url.Parse treats "localhost:11434" as an opaque scheme:opaque pair, not a
host — a schemeless api_base (as migrate.go's legacy importer can write)
silently never got the use_chat_completions_api fix-up. Reparse with a
"//" prefix when Host is empty.

completes plan item #7
EOF
)"
```

---

## Task 5: `service.go` — bound `Sync`'s under-lock `Recheck` probe with a timeout

**Review finding:** `wt/cmd/wt/litellm.go:127` / `wt/internal/litellm/service.go` — `Sync`'s `Recheck` callback performs live HTTP probes against ollama/omlx/mtplx, and `service.go` invokes it from inside the `config.yaml` flock. Failure scenario: a slow/unresponsive provider probe holds the lock for the probe's duration; a concurrent caller with a bounded context (e.g. `routes.go`'s `applyAndReport`, ~15s `settleTimeout`) can hit its own deadline first, surfacing a spurious "route not updated" warning on an ordinary `wt start`/`wt stop` racing a `wt litellm sync`.

**Files:**
- Modify: `wt/internal/litellm/service.go`
- Test: `wt/internal/litellm/service_test.go`

**Interfaces:** `Options.Recheck` keeps its existing `func() []string` signature (no change to `wt/cmd/wt/litellm.go`'s call site). A new package-level `var recheckTimeout = 5 * time.Second` seam is added for tests to shrink.

- [ ] **Step 1: Write the failing test**

Add to `wt/internal/litellm/service_test.go`:

```go
// TestSyncRecheckTimesOutInsteadOfHoldingTheLock pins that a slow/hung
// Recheck probe cannot hold the config.yaml lock indefinitely: past
// recheckTimeout, Sync gives up on the recheck and applies the pre-recheck
// plan, the same as if Recheck were unset. Without this a hung provider
// probe could starve a concurrent bounded-context caller (the lifecycle
// route hook's settling bounce) of the lock.
func TestSyncRecheckTimesOutInsteadOfHoldingTheLock(t *testing.T) {
	old := recheckTimeout
	recheckTimeout = 30 * time.Millisecond
	t.Cleanup(func() { recheckTimeout = old })

	o, _, p := opts(t, `model_list:
  - model_name: ollama/gemma:9b
    litellm_params: {model: ollama_chat/gemma:9b}
`)
	block := make(chan []string) // never sent to: simulates a hung probe
	o.Recheck = func() []string { return <-block }

	start := time.Now()
	if _, err := Sync(testConfig(), nil, o); err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("Sync took %s, want it to give up around recheckTimeout (%s)", elapsed, recheckTimeout)
	}
	f, _ := Open(p)
	if ids := f.RoutedIDs(); len(ids) != 0 {
		t.Fatalf("routed = %v, want the route removed (recheck timed out, pre-recheck plan applied)", ids)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd wt && go test ./internal/litellm -run TestSyncRecheckTimesOutInsteadOfHoldingTheLock -v -timeout 5s`
Expected: the test HANGS (or fails the `-timeout` deadline) because `Sync` currently blocks forever on `o.Recheck()`.

- [ ] **Step 3: Bound the Recheck call**

In `wt/internal/litellm/service.go`, add `"time"` to the imports, then add near the top of the file (after the `Options` struct or near `Sync`):

```go
// recheckTimeout bounds Sync's live Recheck probe while the config.yaml
// lock is held: a slow/unresponsive provider must not hold the lock
// indefinitely and starve a concurrent bounded-context caller (e.g. the
// lifecycle route hook's settling bounce). A timeout is treated the same
// as "no Recheck": the pre-recheck plan proceeds unchanged. A var, not a
// const, so tests can shrink it.
var recheckTimeout = 5 * time.Second

// runRecheck calls o.Recheck with recheckTimeout, reporting ok=false (fresh
// left nil) when it does not return in time.
func runRecheck(o Options) (fresh []string, ok bool) {
	done := make(chan []string, 1)
	go func() { done <- o.Recheck() }()
	select {
	case fresh = <-done:
		return fresh, true
	case <-time.After(recheckTimeout):
		return nil, false
	}
}
```

Then in `Sync`, replace:

```go
		if o.Recheck != nil && (len(add) > 0 || len(remove) > 0) {
			fresh := o.Recheck()
			if len(remove) > 0 {
				remove = slices.DeleteFunc(remove, func(id string) bool { return slices.Contains(fresh, id) })
			}
			if len(add) > 0 {
				add = slices.DeleteFunc(add, func(id string) bool { return !slices.Contains(fresh, id) })
			}
		}
```

with:

```go
		if o.Recheck != nil && (len(add) > 0 || len(remove) > 0) {
			if fresh, ok := runRecheck(o); ok {
				if len(remove) > 0 {
					remove = slices.DeleteFunc(remove, func(id string) bool { return slices.Contains(fresh, id) })
				}
				if len(add) > 0 {
					add = slices.DeleteFunc(add, func(id string) bool { return !slices.Contains(fresh, id) })
				}
			}
		}
```

- [ ] **Step 4: Run the failing test again, then the whole package**

Run: `cd wt && go build ./... && go test ./internal/litellm/... -v`
Expected: `TestSyncRecheckTimesOutInsteadOfHoldingTheLock` PASSes quickly (~30ms), and `TestSyncRecheckKeepsModelsThatStartedMeanwhile` / `TestSyncRecheckAlsoAppliesToAddSet` still pass (their `Recheck` closures return immediately, well under the 5s default).

- [ ] **Step 5: Commit**

```bash
git add wt/internal/litellm/service.go wt/internal/litellm/service_test.go
git commit -m "$(cat <<'EOF'
fix(wt): bound Sync's under-lock Recheck probe with a timeout

A hung provider probe inside Options.Recheck could hold the config.yaml
flock indefinitely, starving a concurrent bounded-context caller. A
timed-out recheck now falls back to the pre-recheck plan instead of
blocking.

completes plan item #8
EOF
)"
```

---

## Task 6: `internal/lifecycle/routes.go` — take the LiteLLM proxy restart off the start/stop critical path

**Review finding:** `wt/internal/lifecycle/routes.go:137` — every local-model start/stop now synchronously runs the full LiteLLM route pipeline in-line: flock, `config.yaml` rewrite, proxy restart (30s timeout), then a readiness poll (30s timeout), before returning to the caller. Failure scenario: a plain `wt start`/`wt stop` that used to return once the backend process was confirmed up can now block for a combined worst case of ~60s on LiteLLM proxy machinery that is off the model-serving critical path.

**Files:**
- Modify: `wt/internal/lifecycle/routes.go`
- Modify: `wt/cmd/wt/main.go` (wait for pending route work before the process exits)
- Test: `wt/internal/lifecycle/routes_test.go`

**Interfaces:**
- `applyAndReport` keeps its existing signature; its restart+readiness-wait phase moves into a goroutine tracked by a package-level `sync.WaitGroup`.
- New exported `lifecycle.WaitPendingRoutes()` — blocks until every in-flight async restart started by `applyAndReport` has finished. Every `wt` command must call this before the process exits, or a restart it kicked off could be killed mid-flight.

- [ ] **Step 1: Write the failing test**

Add to `wt/internal/lifecycle/routes_test.go`:

```go
// TestApplyAndReportRestartsAsynchronously pins that the route write returns
// as soon as config.yaml is saved, without waiting for the proxy restart or
// readiness poll — that machinery is off the model-serving critical path.
// WaitPendingRoutes must still observe it finish before the caller (a wt
// process) is allowed to exit.
func TestApplyAndReportRestartsAsynchronously(t *testing.T) {
	calls, _ := stubRoutes(t, litellm.Result{Changed: true}, nil)
	restartStarted := make(chan struct{})
	restartMayFinish := make(chan struct{})
	restartProxy = func(context.Context) []string {
		close(restartStarted)
		<-restartMayFinish
		return nil
	}

	done := make(chan bool, 1)
	go func() {
		done <- applyAndReport(context.Background(), routesCfg(), []string{"ollama/a:1"}, nil, restartIfChanged)
	}()

	select {
	case changed := <-done:
		if !changed {
			t.Fatal("applyAndReport returned false, want true (config.yaml changed)")
		}
	case <-time.After(time.Second):
		t.Fatal("applyAndReport blocked on the async restart instead of returning first")
	}
	<-restartStarted // the restart did start...
	close(restartMayFinish)
	WaitPendingRoutes() // ...and WaitPendingRoutes must observe it finish
	if len(*calls) != 1 {
		t.Fatalf("route calls = %d, want 1", len(*calls))
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd wt && go test ./internal/lifecycle -run TestApplyAndReportRestartsAsynchronously -v -timeout 5s`
Expected: FAIL/timeout — `applyAndReport` currently blocks in the calling goroutine until `restartProxy` returns, so `done` never fires within 1s (it's waiting on `restartMayFinish`, which the test hasn't closed yet).

- [ ] **Step 3: Read `routes_test.go` in full before changing anything**

`wt/internal/lifecycle/routes_test.go` pins exact context-cancellation semantics that the async redesign must preserve — read the whole file (not just the earlier grep of function names) before writing code, in particular:

- `TestRouteHonorsCallerContext` (~line 202): asserts the ctx `waitProxy` receives on the **normal** (non-`bounceRoutes`) path is a genuine descendant of the caller's `base` ctx — it must carry `base`'s value (`c.Value(ctxKey{})`) AND close its `Done()` channel when the caller later calls `cancel()`. **`context.WithoutCancel` must not be used on this path** — it would strip `Done()` propagation and break this test (and the real behavior it protects: Ctrl+C must reach an in-flight readiness wait, "ignoring ctx would hang the process up to the readiness timeout").
- `TestBounceRoutesSurvivesCancelledContext` (~line 271): asserts `bounceRoutes` runs its restart on a **live** ctx (`ctx.Err() == nil`) even when the caller's ctx is already cancelled — `bounceRoutes`'s existing `context.WithoutCancel(ctx)` call (already in the code, unchanged) is what makes this true; nothing new is needed here beyond not breaking it.
- `TestRouteWarnsWhenProxyWaitFailsUncancelled`, `TestRouteWaitsForProxyOnlyWhenChanged`, `TestRouteSkipsWaitWhenProxyWasNotRunning`, `TestRouteProbesProxyOnlyWhenRestarting`: each asserts on a counter/buffer (`warn`, `waited`, `probed`) that the restart/wait phase sets.

**Ruling (recorded so you don't need to re-derive it):** because `bounceRoutes` already detaches from cancellation itself (`context.WithoutCancel(ctx)`) before calling `applyAndReport`, `applyAndReport`'s own goroutine must NOT also wrap its ctx in another `context.WithoutCancel` — doing so would strip `Done()` propagation on the *normal* path too, breaking `TestRouteHonorsCallerContext`. Instead:
  - `applyAndReport`'s async goroutine derives its context as a plain, cancellation-preserving child: `context.WithTimeout(ctx, proxyAliveTimeout+proxyReadyTimeout)` — no `WithoutCancel` inside `applyAndReport` itself.
  - `bounceRoutes` must stop wrapping its already-detached ctx in its own `context.WithTimeout(..., settleTimeout)` + `defer cancel()` — under the old fully-synchronous code that bound was safe because `bounceRoutes` didn't return until the whole restart finished; under the new async code, `bounceRoutes` returns almost immediately, so its `defer cancel()` would fire within milliseconds and kill the goroutine's ctx (a child of it) before the restart even runs. `bounceRoutes` becomes:

    ```go
    // bounceRoutes restarts the proxy (and waits for it) to settle deferred route
    // writes when the start that followed them failed or found nothing to route.
    // Detached from ctx: the common trigger is the user's Ctrl+C, and a cancelled
    // ctx would kill the settling restart at once, leaving the proxy serving the
    // removed route. No local timeout wrapper here any more — applyAndReport's own
    // async restart step applies its own bound (proxyAliveTimeout+proxyReadyTimeout).
    func bounceRoutes(ctx context.Context, cfg *config.Config) {
    	applyAndReport(context.WithoutCancel(ctx), cfg, nil, nil, restartForced)
    }
    ```
  - The `settleTimeout` const becomes unused by this change — delete it (and its doc comment) rather than leave dead code. Search the package for any other reference before deleting (`grep -rn settleTimeout wt/internal/lifecycle/`) — if genuinely only referenced here, remove it.
  - This means `bounceRoutes`'s synchronous config.yaml write is no longer bounded by `settleTimeout` for the lock-wait specifically (it falls back to `WithLock`'s unbounded-wait path when ctx has no deadline) — an accepted, narrow trade-off: this path only runs after an already-cancelled/failed start's cleanup, a rare case, and the change this task exists to make (get the *restart* off the critical path) is unaffected either way. Mention this trade-off in the task's commit message.

- [ ] **Step 4: Move the restart+wait phase into a tracked goroutine**

In `wt/internal/lifecycle/routes.go`, add `"sync"` to the imports, then add near the other package vars:

```go
// routeWG tracks in-flight async proxy restarts started by applyAndReport.
var routeWG sync.WaitGroup

// WaitPendingRoutes blocks until every async proxy restart started by
// applyAndReport has finished. Every wt command that can reach a route hook
// (start, stop, smoke, the TUI start flow) must call this before the process
// exits — otherwise a restart it kicked off is killed mid-flight and the
// proxy never actually picks up the route change.
func WaitPendingRoutes() {
	routeWG.Wait()
}
```

Then replace `applyAndReport`'s body:

```go
// applyAndReport reports whether config.yaml was written.
func applyAndReport(ctx context.Context, cfg *config.Config, add, remove []string, mode restartMode) bool {
	// The proxy is probed once, lazily, just before a restart (Listening: only
	// a refused connection counts as down, so a slow proxy is still waited
	// for). Waiting for readiness afterwards is only meaningful when a proxy
	// was actually serving: a configured URL with nothing listening (routing
	// may even be switched off) used to cost every start and stop the full
	// proxyReadyTimeout. Probing inside the restart hook means writes that
	// never restart (deferred, or nothing changed) pay no probe at all. The
	// restart itself stays best-effort either way.
	url := cfg.LitellmBaseURL()
	wasUp := false

	res, err := applyRoutes(cfg, add, remove, litellm.Options{
		SkipReadyGate: true,
		NoRestart:     mode == restartDeferred,
		ForceRestart:  mode == restartForced,
		// The caller's ctx bounds the config.yaml lock wait too, so a
		// contended lock cannot outlive this hook's own deadline.
		Ctx: ctx,
		// The caller's ctx, so Ctrl+C during a start/stop does not keep
		// paying for the restart command's full run time.
		Restart: func() []string {
			wasUp = url != "" && probeProxy(ctx, url, proxyAliveTimeout)
			return restartProxy(ctx)
		},
	})
	if err != nil {
		if !errors.Is(err, litellm.ErrMissing) { // no config.yaml = LiteLLM not set up
			fmt.Fprintf(routesWarn, "wt: LiteLLM route not updated: %v\n", err)
		}
		return false
	}
	for _, o := range res.Outcomes {
		if o.Err != nil {
			fmt.Fprintf(routesWarn, "wt: LiteLLM route for %s not updated: %v\n", o.ID, o.Err)
		}
	}
	if ctx.Err() == nil { // cancelled by the user: a failed restart is expected, stay quiet
		for _, w := range res.Warnings {
			fmt.Fprintf(routesWarn, "wt: %s\n", w)
		}
	}
	restarted := (res.Changed && mode != restartDeferred) || mode == restartForced
	if restarted && wasUp {
		if err := waitProxy(ctx, url, proxyReadyTimeout); err != nil && ctx.Err() == nil { // cancelled by the user: stay quiet
			fmt.Fprintf(routesWarn, "wt: %v\n", err)
		}
	}
	return res.Changed
}
```

with:

```go
// applyAndReport writes the route change and reports whether config.yaml
// changed. The proxy restart and readiness wait — the slow part, up to
// proxyAliveTimeout+proxyReadyTimeout — run asynchronously (tracked by
// routeWG / WaitPendingRoutes) so a start/stop returns to its caller as soon
// as the write itself is done, instead of blocking the model-serving path on
// LiteLLM proxy machinery.
func applyAndReport(ctx context.Context, cfg *config.Config, add, remove []string, mode restartMode) bool {
	res, err := applyRoutes(cfg, add, remove, litellm.Options{
		SkipReadyGate: true,
		// The restart is always deferred here: applyAndReport itself decides
		// whether/when to restart, asynchronously, below.
		NoRestart: true,
		// The caller's ctx bounds the config.yaml lock wait, so a contended
		// lock cannot outlive this hook's own deadline.
		Ctx: ctx,
	})
	if err != nil {
		if !errors.Is(err, litellm.ErrMissing) { // no config.yaml = LiteLLM not set up
			fmt.Fprintf(routesWarn, "wt: LiteLLM route not updated: %v\n", err)
		}
		return false
	}
	for _, o := range res.Outcomes {
		if o.Err != nil {
			fmt.Fprintf(routesWarn, "wt: LiteLLM route for %s not updated: %v\n", o.ID, o.Err)
		}
	}
	restart := (res.Changed && mode != restartDeferred) || mode == restartForced
	if !restart {
		return res.Changed
	}
	url := cfg.LitellmBaseURL()
	routeWG.Add(1)
	go func() {
		defer routeWG.Done()
		// A plain child of ctx, not context.WithoutCancel(ctx): the normal
		// (non-bounceRoutes) path must still observe the caller's real
		// cancellation (Ctrl+C) — TestRouteHonorsCallerContext pins this.
		// bounceRoutes already detaches its own ctx before calling in here,
		// so this stays live in that path per TestBounceRoutesSurvivesCancelledContext.
		bgCtx, cancel := context.WithTimeout(ctx, proxyAliveTimeout+proxyReadyTimeout)
		defer cancel()
		wasUp := url != "" && probeProxy(bgCtx, url, proxyAliveTimeout)
		warnings := restartProxy(bgCtx)
		if bgCtx.Err() == nil { // cancelled by the user: a failed restart is expected, stay quiet
			for _, w := range warnings {
				fmt.Fprintf(routesWarn, "wt: %s\n", w)
			}
		}
		if wasUp {
			if err := waitProxy(bgCtx, url, proxyReadyTimeout); err != nil && bgCtx.Err() == nil {
				fmt.Fprintf(routesWarn, "wt: %v\n", err)
			}
		}
	}()
	return res.Changed
}
```

- [ ] **Step 5: Update `stubRoutes` for the new contract, then add `WaitPendingRoutes()` to every test whose assertions depend on the async phase**

`stubRoutes` (top of `routes_test.go`) currently asserts `o.Restart == nil` is an error and invokes `o.Restart()` itself to simulate a restart — both are dead under the new contract, since `applyAndReport` now always passes `NoRestart: true` and never sets `Options.Restart` at all (the restart happens via the `restartProxy`/`probeProxy`/`waitProxy` seams directly, in the goroutine). Replace the fake `applyRoutes` inside `stubRoutes`:

```go
	applyRoutes = func(_ *config.Config, add, remove []string, o litellm.Options) (litellm.Result, error) {
		if !o.SkipReadyGate {
			t.Error("lifecycle hook must skip the ready gate: the model is verifiably running")
		}
		if o.Restart == nil {
			t.Error("lifecycle hook must pass a ctx-aware restart, not leave the package default")
		}
		calls = append(calls, routeCall{add, remove})
		// Like the real Apply: the restart hook runs only when a restart
		// happens (this is where the lazy proxy probe lives).
		if err == nil && !o.NoRestart && (res.Changed || o.ForceRestart) {
			o.Restart()
		}
		return res, err
	}
```

with:

```go
	applyRoutes = func(_ *config.Config, add, remove []string, o litellm.Options) (litellm.Result, error) {
		if !o.SkipReadyGate {
			t.Error("lifecycle hook must skip the ready gate: the model is verifiably running")
		}
		if !o.NoRestart {
			t.Error("applyAndReport must always defer the restart (NoRestart) and run it asynchronously itself")
		}
		calls = append(calls, routeCall{add, remove})
		return res, err
	}
```

Then, reading each test in the file that stubs `restartProxy`/`waitProxy`/`probeProxy` and asserts on something those stubs set (a counter, `warn`'s contents, a captured ctx list) — `TestRouteHonorsCallerContext`, `TestRouteWarnsWhenProxyWaitFailsUncancelled`, `TestRouteWaitsForProxyOnlyWhenChanged`, `TestRouteSkipsWaitWhenProxyWasNotRunning`, `TestBounceRoutesSurvivesCancelledContext`, `TestRouteProbesProxyOnlyWhenRestarting` — insert a `lifecycle.WaitPendingRoutes()` call (same package, so just `WaitPendingRoutes()`) immediately before each such assertion, right after the `routeAfterStart`/`routeAfterStop`/`bounceRoutes`/`routeRemove` call whose async side effect that assertion depends on. `TestRouteHonorsCallerContext` needs two such calls (after its first `routeAfterStart`/`routeAfterStop` pair, before checking `captured`, and again after its second pair, before checking `warn`). `TestRouteAfterStartUnregisteredSettlesOwedRestart` and `TestRouteAfterStartSingleModelReplacesSiblings`/`TestRouteAfterStartMultiTenantOnlyAdds`/`TestRouteAfterStop` (which only assert on `calls`, set synchronously before the goroutine spawns) do **not** need it — verify by running the full suite with `-race` (Step 7) and fixing any test that still flakes.

- [ ] **Step 6: Make every `wt` command wait for pending route work before exiting**

Read `wt/cmd/wt/main.go` to find where the root `cobra.Command.Execute()` (or equivalent) is invoked and the process is about to return/os.Exit. Add a call to `lifecycle.WaitPendingRoutes()` immediately after that invocation returns and before the exit code is applied, e.g.:

```go
err := rootCmd.Execute()
lifecycle.WaitPendingRoutes() // don't exit while an async proxy restart (routes.go) is still in flight
if err != nil {
    os.Exit(1)
}
```

(Match the exact existing structure/imports of `main.go` — add the `internal/lifecycle` import if not already present.)

- [ ] **Step 7: Run the failing test again, then the whole module with `-race`**

Run: `cd wt && go build ./... && go vet ./... && go test -race ./...`
Expected: `TestApplyAndReportRestartsAsynchronously` PASSes, and every existing `internal/lifecycle` route test (`TestRouteAfterStartSingleModelReplacesSiblings`, `TestRouteWaitsForProxyOnlyWhenChanged`, `TestRouteHonorsCallerContext`, `TestRouteWarnsWhenProxyWaitFailsUncancelled`, `TestBounceRoutesSurvivesCancelledContext`, etc.) still passes with no `-race` reports. `-race` matters here specifically because Step 5 just introduced real goroutines writing to shared test state (`warn`, counters, `captured`) — a missing `WaitPendingRoutes()` call shows up as a race warning even on a run that happens not to fail its plain assertions.

- [ ] **Step 8: Commit**

```bash
git add wt/internal/lifecycle/routes.go wt/cmd/wt/main.go wt/internal/lifecycle/routes_test.go
git commit -m "$(cat <<'EOF'
fix(wt): run the LiteLLM proxy restart/readiness-wait asynchronously

start/stop no longer block on the proxy restart and readiness poll
(up to ~60s combined) after writing config.yaml — that work now runs in
a tracked goroutine, and every wt command waits for it via
lifecycle.WaitPendingRoutes() right before the process exits so the
restart still always completes.

bounceRoutes' own settleTimeout wrapper is removed: under synchronous
execution it safely bounded the whole settling restart, but under async
execution its defer-cancel would fire the instant bounceRoutes returns
and kill the goroutine's context before the restart ran. The restart
phase now applies its own bound instead; bounceRoutes' synchronous
config.yaml write (the lock wait specifically) is consequently
unbounded on this already-rare failed/cancelled-start cleanup path.

completes plan item #9
EOF
)"
```

---

## Task 7: `wt_bridge.py` — reconcile a timed-out expose/unexpose instead of assuming nothing applied

**Review finding:** `modelman/src/modelman/wt_bridge.py:74` — `wt litellm expose/unexpose` writes `config.yaml` and restarts the proxy before printing its JSON result; if the subprocess is killed on the bridge's timeout after that write succeeds, modelman treats the whole call as failed. Failure scenario: a slow/killed `wt` process after a successful `config.yaml` write leaves `modelman.toml`'s `exposed` flag out of sync with the real routing state, undetected until an explicit reconcile.

**Files:**
- Modify: `modelman/src/modelman/wt_bridge.py`
- Test: `modelman/tests/test_wt_bridge.py`

**Interfaces:**
- New `class WtBridgeTimeoutError(WtBridgeError)` — raised by `_run()` in place of the generic `WtBridgeError` for a `subprocess.TimeoutExpired`.
- `_change()` catches `WtBridgeTimeoutError` and calls a new `_reconcile_after_timeout(args, litellm_path) -> BridgeResult` helper.

- [ ] **Step 1: Write the failing test**

Add to `modelman/tests/test_wt_bridge.py`:

```python
def test_expose_timeout_reconciles_via_routed_ids(monkeypatch):
    # A timed-out `wt litellm expose` may have already written config.yaml
    # and restarted the proxy before the kill — the bridge must check the
    # actual routed set (`wt litellm list`, unaffected by the timed-out
    # call) instead of assuming nothing applied, so a timeout can't
    # silently leave modelman.toml's exposed flag out of sync with the
    # real route.
    from modelman import wt_bridge

    def fake_run(args, env=None, timeout=120):
        if args[0] == "expose":
            raise wt_bridge.WtBridgeTimeoutError("wt litellm expose timed out after 120s")
        raise AssertionError(f"unexpected _run call: {args}")

    monkeypatch.setattr(wt_bridge, "_run", fake_run)
    monkeypatch.setattr(wt_bridge, "routed_ids", lambda litellm_path=None: ["m1"])

    result = wt_bridge.expose(["m1", "m2"])
    outcomes = {o.id: o for o in result.outcomes}
    assert outcomes["m1"].action == "exposed" and outcomes["m1"].error is None
    assert outcomes["m2"].action is None and "timed out" in outcomes["m2"].error


def test_expose_timeout_reconcile_failure_still_raises(monkeypatch):
    # If the post-timeout reconciliation read (`wt litellm list`) itself
    # fails, the caller must still see a clear error rather than a
    # silently-wrong success/failure guess.
    from modelman import wt_bridge

    def fake_run(args, env=None, timeout=120):
        if args[0] == "expose":
            raise wt_bridge.WtBridgeTimeoutError("wt litellm expose timed out after 120s")
        raise AssertionError(f"unexpected _run call: {args}")

    def broken_routed_ids(litellm_path=None):
        raise wt_bridge.WtBridgeError("wt litellm list failed")

    monkeypatch.setattr(wt_bridge, "_run", fake_run)
    monkeypatch.setattr(wt_bridge, "routed_ids", broken_routed_ids)

    with pytest.raises(wt_bridge.WtBridgeError, match="timed out and its result is unknown"):
        wt_bridge.expose(["m1"])
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd modelman && uv run pytest tests/test_wt_bridge.py -k timeout_reconcil -v`
Expected: `AttributeError: module 'modelman.wt_bridge' has no attribute 'WtBridgeTimeoutError'`.

- [ ] **Step 3: Add `WtBridgeTimeoutError` and reconciliation**

In `modelman/src/modelman/wt_bridge.py`, replace:

```python
class WtNotFoundError(WtBridgeError):
    """The wt binary is not on PATH."""
```

with:

```python
class WtNotFoundError(WtBridgeError):
    """The wt binary is not on PATH."""


class WtBridgeTimeoutError(WtBridgeError):
    """wt timed out; a write it was doing (expose/unexpose) may have already
    applied to config.yaml before the kill."""
```

Replace, in `_run`:

```python
    except subprocess.TimeoutExpired:
        raise WtBridgeError(f"wt litellm {sub} timed out after {timeout:g}s") from None
```

with:

```python
    except subprocess.TimeoutExpired:
        raise WtBridgeTimeoutError(f"wt litellm {sub} timed out after {timeout:g}s") from None
```

Replace `_change`:

```python
def _change(args: list[str], litellm_path: Path | None) -> BridgeResult:
    proc = _run(args, _env(litellm_path))
    try:
        json.loads(proc.stdout)
    except (ValueError, TypeError):
        # No JSON on stdout: a file-level failure (missing/invalid config.yaml,
        # unreadable registry). Nothing was applied.
        raise WtBridgeError(_msg(proc, f"wt exited {proc.returncode}")) from None
    try:
        # Parse regardless of exit code: a partial batch exits 1 with JSON.
        return parse_change_result(proc.stdout)
    except (ValueError, KeyError, TypeError, AttributeError):
        raise WtBridgeError(
            f"unparseable wt output (exit {proc.returncode}); changes may have been applied"
        ) from None
```

with:

```python
def _change(args: list[str], litellm_path: Path | None) -> BridgeResult:
    try:
        proc = _run(args, _env(litellm_path))
    except WtBridgeTimeoutError:
        return _reconcile_after_timeout(args, litellm_path)
    try:
        json.loads(proc.stdout)
    except (ValueError, TypeError):
        # No JSON on stdout: a file-level failure (missing/invalid config.yaml,
        # unreadable registry). Nothing was applied.
        raise WtBridgeError(_msg(proc, f"wt exited {proc.returncode}")) from None
    try:
        # Parse regardless of exit code: a partial batch exits 1 with JSON.
        return parse_change_result(proc.stdout)
    except (ValueError, KeyError, TypeError, AttributeError):
        raise WtBridgeError(
            f"unparseable wt output (exit {proc.returncode}); changes may have been applied"
        ) from None


def _reconcile_after_timeout(args: list[str], litellm_path: Path | None) -> BridgeResult:
    """expose/unexpose timed out: config.yaml may already have been written
    (and the proxy restarted) before the kill. Read the actual routed set
    back with a plain `wt litellm list` — unaffected by the timed-out call —
    instead of assuming nothing applied, so a timeout can't silently leave
    modelman.toml's exposed flag out of sync with the real route."""
    action = args[0]
    ids = args[args.index("--") + 1 :]
    try:
        routed = set(routed_ids(litellm_path=litellm_path))
    except WtBridgeError:
        raise WtBridgeError(f"wt litellm {action} timed out and its result is unknown") from None
    applied_action = "exposed" if action == "expose" else "unexposed"
    outcomes = []
    for model_id in ids:
        applied = (model_id in routed) if action == "expose" else (model_id not in routed)
        outcomes.append(
            BridgeOutcome(
                model_id,
                applied_action if applied else None,
                None if applied else f"wt litellm {action} timed out before this id applied",
            )
        )
    return BridgeResult(
        outcomes=outcomes,
        changed=True,
        warnings=[f"wt litellm {action} timed out; recovered actual state from config.yaml"],
    )
```

- [ ] **Step 4: Run the failing tests again, then the whole file**

Run: `cd modelman && uv run pytest tests/test_wt_bridge.py -v`
Expected: both new tests PASS, and `test_run_converts_subprocess_failures_without_leaking_argv`'s `TimeoutExpired` case still PASSes (it only asserts `isinstance(..., WtBridgeError)`, which `WtBridgeTimeoutError` satisfies, and the message shape is unchanged).

- [ ] **Step 5: Commit**

```bash
git add modelman/src/modelman/wt_bridge.py modelman/tests/test_wt_bridge.py
git commit -m "$(cat <<'EOF'
fix(modelman): reconcile a timed-out expose/unexpose via routed_ids

A killed/slow wt after it already wrote config.yaml previously made
modelman assume nothing applied, drifting the exposed flag from the
real route. A timeout now triggers a `wt litellm list` read-back to
recover the actual per-id outcome.

completes plan item #4 (wt_bridge half)
EOF
)"
```

---

## Task 8: `litellm.py` — `apply_expose_queue` must not let a bridge failure wipe already-computed per-id errors

**Review finding:** `modelman/src/modelman/litellm.py:458` — the expose-batch bridge call in `apply_expose_queue` is unwrapped (unlike the unexpose batch, fixed in `a76b9d5`), and a `LiteLLMConfigError` from `_provider_flags_for_write()` (via `_validate_locally`, in the queue's validation loop) isn't caught by that loop's `except ExposeError` either — both propagate out of the whole function. Failure scenario: a queue with one model already flagged invalid plus others pending expose — if the expose-batch bridge call fails (or wt is transiently unreachable during validation), the exception wipes the already-computed `errors` and the caller sees the same bridge-failure reason for every id, masking the real per-id cause.

**Files:**
- Modify: `modelman/src/modelman/litellm.py`
- Test: `modelman/tests/test_expose.py`

**Interfaces:** `apply_expose_queue`'s signature and return type are unchanged; it now never raises `LiteLLMConfigError` (matches `apply_unexpose_queue`'s existing no-raise contract) — every bridge-level failure becomes a per-id error string in the returned `outcomes`.

- [ ] **Step 1: Write the failing tests**

Add to `modelman/tests/test_expose.py` (check the file's existing fixtures/imports for `apply_expose_queue`, `Registry`/`StateStore` construction helpers, and `wt_bridge` patching conventions already used by its other `apply_expose_queue`/`apply_unexpose_queue` tests, and match them):

```python
def test_apply_expose_queue_bridge_failure_in_expose_batch_is_per_id(tmp_path, calls):
    # A bridge-level failure in the EXPOSE batch must not raise and wipe
    # already-computed errors from other ids in the same queue — it becomes
    # a per-id error for the expose batch's own ids only, mirroring the fix
    # already applied to the unexpose batch (a76b9d5).
    from modelman import wt_bridge

    def broken_expose(ids, *, litellm_path=None, skip_ready_gate=True):
        raise wt_bridge.WtBridgeError("wt not found")

    registry, state = ...  # build per the file's existing fixture pattern:
    # one unready model "bad" (fails _validate_locally locally) and one
    # ready, mapped model "good" (reaches the expose batch).
    monkeypatch.setattr(wt_bridge, "expose", broken_expose)

    outcomes, warnings = apply_expose_queue(
        registry, state, [("bad", True), ("good", True)], tmp_path / "config.yaml"
    )
    by_id = {model_id: error for model_id, _target, error in outcomes}
    assert "not ready" in by_id["bad"]
    assert "wt not found" in by_id["good"]


def test_apply_expose_queue_validation_bridge_error_does_not_abort_the_loop(tmp_path, calls):
    # _validate_locally can itself raise LiteLLMConfigError (via
    # _provider_flags_for_write, when wt's provider table can't be read at
    # all) — that must become a per-id error too, not abort validation for
    # every remaining id in the queue.
    from modelman import wt_bridge

    monkeypatch.setattr(wt_bridge, "provider_cloud_flags", lambda: {})  # wt unreachable

    registry, state = ...  # two ready, mapped models "a" and "b"
    outcomes, warnings = apply_expose_queue(
        registry, state, [("a", True), ("b", True)], tmp_path / "config.yaml"
    )
    by_id = {model_id: error for model_id, _target, error in outcomes}
    assert "cannot read wt's LiteLLM provider table" in by_id["a"]
    assert "cannot read wt's LiteLLM provider table" in by_id["b"]
```

(Replace the `...` fixture placeholders with whatever `Registry`/`StateStore`/`ModelEntry` construction the file's existing `apply_expose_queue` tests already use — read `test_expose.py` in full before writing these so the fixtures match exactly; do not invent a different construction style.)

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd modelman && uv run pytest tests/test_expose.py -k apply_expose_queue -v`
Expected: the first new test raises `WtBridgeError`/`LiteLLMConfigError` out of `apply_expose_queue` instead of returning outcomes; the second raises out of the validation loop for the first id and never reaches the second.

- [ ] **Step 3: Fix the validation loop and the expose batch**

In `modelman/src/modelman/litellm.py`, in `apply_expose_queue`, replace:

```python
    for model_id, target in exposes:
        if not target:
            to_remove.append(model_id)
            continue
        try:
            _validate_locally(registry, state, model_id)
        except ExposeError as exc:
            errors[model_id] = str(exc)
            continue
        to_add.append(model_id)
```

with:

```python
    for model_id, target in exposes:
        if not target:
            to_remove.append(model_id)
            continue
        try:
            _validate_locally(registry, state, model_id)
        except (ExposeError, LiteLLMConfigError) as exc:
            errors[model_id] = str(exc)
            continue
        to_add.append(model_id)
```

Replace:

```python
    warnings: list[str] = []
    if to_add:
        result = _bridge(wt_bridge.expose, to_add, litellm_path=litellm_path, skip_ready_gate=True)
        warnings += result.warnings
        for model_id in to_add:
            if (error := _outcome_error(result, model_id)) is not None:
                errors[model_id] = error
    if to_remove:
```

with:

```python
    warnings: list[str] = []
    if to_add:
        try:
            result = _bridge(wt_bridge.expose, to_add, litellm_path=litellm_path, skip_ready_gate=True)
        except LiteLLMConfigError as exc:
            # Bridge-level failure in the expose batch — nothing in it was
            # applied. Per-id error rather than raising, so it can't wipe
            # errors already recorded above for other ids in this queue.
            for model_id in to_add:
                errors[model_id] = str(exc)
        else:
            warnings += result.warnings
            for model_id in to_add:
                if (error := _outcome_error(result, model_id)) is not None:
                    errors[model_id] = error
    if to_remove:
```

Then update the function's docstring paragraph — replace:

```
    An id that fails modelman's own gate never reaches wt.

    Returns (outcomes, warnings). `outcomes` is one (model_id, target,
    error) per queue item, in the queue's order, error None when applied.
    A bridge-level failure (wt missing, unreadable config.yaml) in the
    expose batch propagates to the caller as LiteLLMConfigError with no
    flag touched — nothing was applied. In the unexpose batch the exposes
    have already applied, so the same failure becomes a per-id error for
    each unexpose instead of raising: the applied exposes' flags still
    flip and the caller sees exactly which ids failed. Per-model
    validation failures — modelman's or wt's — are reported per item and
    don't block the rest of the queue. Flags flip only after wt reports
    success for that id. `warnings` carries wt's non-fatal proxy-restart
    notices from both batches so the caller can surface them on the UI
    thread instead of printing to stderr from a worker thread.
```

with:

```
    An id that fails modelman's own gate — including wt's provider table
    being unreadable (`LiteLLMConfigError` from `_validate_locally`) —
    never reaches wt and is recorded as a per-id error instead of aborting
    validation for the rest of the queue.

    Returns (outcomes, warnings). `outcomes` is one (model_id, target,
    error) per queue item, in the queue's order, error None when applied.
    apply_expose_queue never raises: a bridge-level failure (wt missing,
    unreadable config.yaml) in either batch becomes a per-id error for
    that batch's own ids, so ids already resolved — validated locally, or
    applied in the other batch — are unaffected, and the caller sees
    exactly which ids failed and why. Per-model validation failures —
    modelman's or wt's — are reported per item and don't block the rest
    of the queue. Flags flip only after wt reports success for that id.
    `warnings` carries wt's non-fatal proxy-restart notices from both
    batches so the caller can surface them on the UI thread instead of
    printing to stderr from a worker thread.
```

- [ ] **Step 4: Run the failing tests again, then the whole file**

Run: `cd modelman && uv run pytest tests/test_expose.py -v`
Expected: both new tests PASS, and every existing `apply_expose_queue`/`apply_unexpose_queue` test still passes unchanged.

- [ ] **Step 5: Commit**

```bash
git add modelman/src/modelman/litellm.py modelman/tests/test_expose.py
git commit -m "$(cat <<'EOF'
fix(modelman): apply_expose_queue no longer lets a bridge error wipe
already-computed per-id errors

Mirrors the unexpose-batch fix from a76b9d5: a bridge-level failure in
the expose batch, or a LiteLLMConfigError from the validation loop, now
becomes a per-id error instead of raising and discarding errors already
recorded for other ids in the same queue.

completes plan item #4 (litellm.py half)
EOF
)"
```

---

## Task 9: `screens/models.py` — fetch provider flags and LiteLLM status concurrently at mount

**Review finding:** `modelman/src/modelman/screens/models.py:298` — `on_mount()` runs `self.reload()` (which triggers `provider_cloud_flags()`, bounded 5s) and `_update_litellm_status()` (`litellm_status()`, bounded 5s) synchronously and back-to-back on the main thread, before the background reconcile worker starts. Failure scenario: on first TUI launch, or whenever both caches have expired, mounting the Models screen blocks up to ~10s before it paints.

**Files:**
- Modify: `modelman/src/modelman/screens/models.py`
- Test: `modelman/tests/screens/test_models.py`

**Interfaces:**
- New `ModelScreen._fetch_litellm_status(self) -> None` — the blocking read half of `_update_litellm_status`, factored out so both `on_mount`'s prefetch and (Task 10's) worker-thread toggle can call the blocking part without also triggering a render from the wrong thread.
- `_update_litellm_status()` becomes `self._fetch_litellm_status(); self._render_litellm_status()` — same externally-observable behavior.
- New `ModelScreen._prefetch_litellm_state(self) -> None` — runs the provider-flags warm-up and the status fetch concurrently via a small thread pool.

- [ ] **Step 1: Write the failing test**

Add to `modelman/tests/screens/test_models.py`:

```python
def test_on_mount_prefetches_litellm_state_concurrently(tmp_path, monkeypatch):
    # provider_cloud_flags() and litellm_status() must run CONCURRENTLY at
    # mount, not back-to-back — two cold, 5s-bounded subprocess reads run
    # one after another would block the initial paint for up to ~10s.
    # Both stubs sleep briefly and are timed; concurrent execution keeps
    # the wall-clock total close to one sleep instead of the sum of both.
    import time as time_mod

    from modelman import wt_bridge

    _seed_registry_and_state(tmp_path, monkeypatch)

    def slow_flags():
        time_mod.sleep(0.3)
        return {"ollama": False}

    def slow_status(timeout=None):
        time_mod.sleep(0.3)
        return wt_bridge.LitellmStatus(True, "http://localhost:4000", True)

    monkeypatch.setattr(wt_bridge, "provider_cloud_flags", slow_flags)
    monkeypatch.setattr(wt_bridge, "litellm_status", slow_status)

    app = ModelmanApp()
    start = time_mod.monotonic()
    async def run():
        async with app.run_test() as pilot:
            await pilot.pause()
            await _open_model_screen(pilot)
    import asyncio

    asyncio.run(run())
    elapsed = time_mod.monotonic() - start
    assert elapsed < 0.55, f"mount took {elapsed:.2f}s, want the two reads run concurrently (~0.3s)"
```

(If the test file already has an `asyncio_mode = "auto"` pytest-asyncio setup that makes a plain non-`async def` test awkward to drive `app.run_test()` from, instead write this as a normal `@pytest.mark.asyncio async def test_...` and time the block with `time.monotonic()` calls immediately before/after the `async with app.run_test() as pilot: ...` block, matching the file's existing `@pytest.mark.asyncio` tests — do not introduce a nested `asyncio.run()` inside an already-async test runner.)

- [ ] **Step 2: Run test to verify it fails**

Run: `cd modelman && uv run pytest tests/screens/test_models.py -k prefetches_litellm_state_concurrently -v`
Expected: FAIL — elapsed is ~0.6s (two sequential 0.3s sleeps), not under 0.55s.

- [ ] **Step 3: Factor `_fetch_litellm_status` and add concurrent prefetch**

In `modelman/src/modelman/screens/models.py`, add `from concurrent.futures import ThreadPoolExecutor` to the imports.

Replace:

```python
    def _update_litellm_status(self) -> None:
        """Re-read wt's routing state once and render it in the status area.

        wt owns the [litellm] state; this asks wt (never modelman.toml) and
        caches the answer in `_litellm_status` so keystrokes never spawn wt.
        Called on mount and after a toggle only. Never raises: an unreachable
        wt renders "unavailable". Purely display: never touches the proxy.
        """
        try:
            self._litellm_status = wt_bridge.litellm_status(timeout=wt_bridge.STATUS_TIMEOUT)
        except wt_bridge.WtBridgeError:
            self._litellm_status = None
        self._render_litellm_status()
```

with:

```python
    def _fetch_litellm_status(self) -> None:
        """Blocking read of wt's routing state into `_litellm_status`
        (bounded by `wt_bridge.STATUS_TIMEOUT`). Never raises: an unreachable
        wt is recorded as None. Split out of `_update_litellm_status` so a
        caller running off the main thread (the mount-time prefetch, the
        toggle worker) can do the blocking read without also rendering from
        the wrong thread."""
        try:
            self._litellm_status = wt_bridge.litellm_status(timeout=wt_bridge.STATUS_TIMEOUT)
        except wt_bridge.WtBridgeError:
            self._litellm_status = None

    def _update_litellm_status(self) -> None:
        """Re-read wt's routing state once and render it in the status area.

        wt owns the [litellm] state; this asks wt (never modelman.toml) and
        caches the answer in `_litellm_status` so keystrokes never spawn wt.
        Called on mount and after a toggle only. Never raises: an unreachable
        wt renders "unavailable". Purely display: never touches the proxy.
        """
        self._fetch_litellm_status()
        self._render_litellm_status()

    def _prefetch_litellm_state(self) -> None:
        """Warm the provider-flags cache and read wt's status CONCURRENTLY
        instead of back-to-back: each is a subprocess call bounded at ~5s
        when its cache is cold, and running them one after another could
        block the initial paint for up to ~10s. Blocks until both are done
        (still synchronous overall), but the wall-clock cost is the slower
        of the two, not their sum."""
        with ThreadPoolExecutor(max_workers=2) as pool:
            status_future = pool.submit(self._fetch_litellm_status)
            flags_future = pool.submit(wt_bridge.provider_cloud_flags)
            status_future.result()
            flags_future.result()
```

Then in `on_mount`, replace:

```python
        self.reload()
        self._refresh_pending_bar()
        self._update_litellm_status()
        mt.focus()
```

with:

```python
        self._prefetch_litellm_state()
        self.reload()
        self._refresh_pending_bar()
        self._render_litellm_status()
        mt.focus()
```

- [ ] **Step 4: Run the failing test again, then the whole screen test file**

Run: `cd modelman && uv run pytest tests/screens/test_models.py -v`
Expected: the new test PASSes (~0.3s), and `test_litellm_status_mount_read_uses_short_timeout_and_degrades` still PASSes (it still asserts the mount-time status read uses `STATUS_TIMEOUT`, unaffected by running it in a pool thread instead of inline).

- [ ] **Step 5: Run the broader modelman suite once for this task**

Run: `cd modelman && make check && uv run pytest tests/screens/ tests/test_expose.py -q`
Expected: no regressions.

- [ ] **Step 6: Commit**

```bash
git add modelman/src/modelman/screens/models.py modelman/tests/screens/test_models.py
git commit -m "$(cat <<'EOF'
fix(modelman): fetch provider flags and LiteLLM status concurrently at mount

on_mount previously ran two independent 5s-bounded wt subprocess reads
back-to-back, blocking the initial paint for up to ~10s when both
caches were cold. They now run concurrently in a small thread pool.

completes plan item #10
EOF
)"
```

---

## Task 10: `screens/models.py` — run the `l` (toggle LiteLLM routing) keybinding off the main thread

**Review finding:** `modelman/src/modelman/screens/models.py:408` — `action_toggle_litellm` (the `l` keybinding) calls `wt_bridge.litellm_set_enabled()` synchronously on Textual's event loop with no timeout override, unlike start/stop which use `run_worker(thread=True)`. Failure scenario: a hung or slow `wt litellm on/off` (includes a proxy restart) freezes the entire TUI — no repaint, no keybindings, not even quit — for up to the bridge's 120s default timeout.

**Files:**
- Modify: `modelman/src/modelman/screens/models.py`
- Test: `modelman/tests/screens/test_models.py`

**Interfaces:**
- `action_toggle_litellm` now dispatches to a new `ModelScreen._do_toggle_litellm(self, target: bool) -> None`, run via `self.run_worker(..., thread=True, group="litellm-toggle")` — a distinct worker group from `"reconcile"`/`"model-start"`/`"model-stop"` so it can't cancel or be cancelled by them.
- Reuses `_fetch_litellm_status()` from Task 9.

- [ ] **Step 1: Update the two existing toggle tests to poll for the worker (they currently assume synchronous completion)**

In `modelman/tests/screens/test_models.py`, in `test_litellm_status_line_and_toggle_go_through_wt`, replace:

```python
        await pilot.press("l")
        await pilot.pause()
        assert calls == [False]
        assert "LiteLLM: off" in _status_text(screen)
```

with:

```python
        await pilot.press("l")
        for _ in range(200):
            await pilot.pause()
            if calls:
                break
        assert calls == [False]
        assert "LiteLLM: off" in _status_text(screen)
```

In `test_litellm_status_unavailable_and_toggle_error_notifies`, replace both:

```python
        await pilot.press("l")
        await pilot.pause()
        assert notes and "unavailable" in notes[-1]
```

with:

```python
        await pilot.press("l")
        for _ in range(200):
            await pilot.pause()
            if notes:
                break
        assert notes and "unavailable" in notes[-1]
```

and:

```python
        monkeypatch.setattr(wt_bridge, "litellm_set_enabled", fail)
        await pilot.press("l")
        await pilot.pause()
        assert "wt exited 1" in notes[-1]
```

with:

```python
        monkeypatch.setattr(wt_bridge, "litellm_set_enabled", fail)
        base = len(notes)
        await pilot.press("l")
        for _ in range(200):
            await pilot.pause()
            if len(notes) > base:
                break
        assert "wt exited 1" in notes[-1]
```

- [ ] **Step 2: Run these two tests to confirm they still pass against the CURRENT (synchronous) code**

Run: `cd modelman && uv run pytest tests/screens/test_models.py -k "litellm_status_line_and_toggle or litellm_status_unavailable_and_toggle" -v`
Expected: both PASS (polling for an already-synchronous result is a no-op — the loop breaks on its first iteration).

- [ ] **Step 3: Write a new test proving the toggle no longer blocks the event loop**

Add to `modelman/tests/screens/test_models.py`:

```python
@pytest.mark.asyncio
async def test_toggle_litellm_runs_off_the_main_thread(tmp_path, monkeypatch):
    # A slow wt litellm on/off (a proxy restart can take a while) must not
    # freeze the TUI: the app must keep processing messages (here, a
    # keystroke on an unrelated binding) while the toggle's subprocess call
    # is still "in flight" on its worker thread.
    import threading

    from modelman import wt_bridge

    _seed_registry_and_state(tmp_path, monkeypatch)
    release = threading.Event()
    entered = threading.Event()

    def status(timeout=None):
        return wt_bridge.LitellmStatus(True, "http://localhost:4000", True)

    def slow_set_enabled(on):
        entered.set()
        release.wait(timeout=5)

    monkeypatch.setattr(wt_bridge, "litellm_status", status)
    monkeypatch.setattr(wt_bridge, "litellm_set_enabled", slow_set_enabled)

    app = ModelmanApp()
    async with app.run_test() as pilot:
        await pilot.pause()
        await _open_model_screen(pilot)
        await pilot.press("l")
        # The event loop must still be alive and processing keystrokes while
        # the worker thread is blocked in slow_set_enabled.
        for _ in range(200):
            await pilot.pause()
            if entered.is_set():
                break
        assert entered.is_set(), "toggle never reached the (slow) bridge call"
        await pilot.press("j")  # an unrelated, harmless keystroke
        await pilot.pause()
        assert not app.is_dead if hasattr(app, "is_dead") else True
    release.set()
```

(If `app.is_dead` isn't a real attribute in this Textual version, drop that assertion line — the meaningful assertions are that `entered.is_set()` is reached and that `pilot.press("j")`/`pilot.pause()` return promptly instead of hanging, which the test's own completion within pytest's default timeout already demonstrates.)

- [ ] **Step 4: Run test to verify it fails / hangs**

Run: `cd modelman && uv run pytest tests/screens/test_models.py -k toggle_litellm_runs_off_the_main_thread -v --timeout=10`
Expected: the test hangs past its timeout — `action_toggle_litellm` currently calls `wt_bridge.litellm_set_enabled` inline on the same thread `pilot.pause()` needs to make progress, so `entered.is_set()` is never observed before the whole test loop deadlocks. (Add `pytest-timeout` usage only if the project already depends on it — check `pyproject.toml`; if not available, rely on the surrounding test suite's default per-test wall time instead of a `--timeout` flag.)

- [ ] **Step 5: Move the toggle onto a worker thread**

In `modelman/src/modelman/screens/models.py`, replace:

```python
    def action_toggle_litellm(self) -> None:
        """Flip the routing switch on `l` by asking wt (the state's owner).
        Routing policy only — the proxy is never touched, modelman.toml is
        never written and self.state is left alone. The cached status is
        refreshed afterwards."""
        status = self._litellm_status
        if status is None:
            # Unknown current state: re-read before deciding the direction.
            self._update_litellm_status()
            status = self._litellm_status
        if status is None:
            self.app.notify("LiteLLM: wt is unavailable — cannot toggle (install wt)")
            return
        try:
            wt_bridge.litellm_set_enabled(not status.enabled)
        except wt_bridge.WtBridgeError as exc:
            self.app.notify(f"LiteLLM toggle failed: {exc}")
            return
        self._update_litellm_status()
```

with:

```python
    def action_toggle_litellm(self) -> None:
        """Flip the routing switch on `l` by asking wt (the state's owner),
        off the main thread: `wt litellm on/off` can include a full proxy
        restart and the bridge's default timeout is 120s, so running it
        inline would freeze the whole TUI (no repaint, no keybindings, not
        even quit) for up to that long. Routing policy only — the proxy is
        never touched, modelman.toml is never written and self.state is
        left alone."""
        status = self._litellm_status
        if status is None:
            # Unknown current state: re-read before deciding the direction.
            # This one status read is still inline (bounded ~5s, not 120s).
            self._update_litellm_status()
            status = self._litellm_status
        if status is None:
            self.app.notify("LiteLLM: wt is unavailable — cannot toggle (install wt)")
            return
        target = not status.enabled
        self.run_worker(
            lambda: self._do_toggle_litellm(target),
            thread=True,
            exclusive=True,
            group="litellm-toggle",
            name="litellm-toggle",
            description="Toggling LiteLLM routing",
        )

    def _do_toggle_litellm(self, target: bool) -> None:
        try:
            wt_bridge.litellm_set_enabled(target)
        except wt_bridge.WtBridgeError as exc:
            self.app.call_from_thread(self.app.notify, f"LiteLLM toggle failed: {exc}")
            return
        self._fetch_litellm_status()
        self.app.call_from_thread(self._render_litellm_status)
```

- [ ] **Step 6: Run the new test and the two updated tests, then the whole screen test file**

Run: `cd modelman && uv run pytest tests/screens/test_models.py -v`
Expected: `test_toggle_litellm_runs_off_the_main_thread` PASSes, `test_litellm_status_line_and_toggle_go_through_wt` and `test_litellm_status_unavailable_and_toggle_error_notifies` still PASS (their polling loops break on the first `pilot.pause()` in practice since the stubbed calls are instant), and no other test in the file regresses.

- [ ] **Step 7: Run the full modelman suite once (final task)**

Run: `cd modelman && make all`
Expected: format + full test suite + lint/typecheck all pass.

- [ ] **Step 8: Commit**

```bash
git add modelman/src/modelman/screens/models.py modelman/tests/screens/test_models.py
git commit -m "$(cat <<'EOF'
fix(modelman): run the 'l' LiteLLM toggle off the main thread

wt litellm on/off can include a full proxy restart and the bridge's
default timeout is 120s; running it inline froze the whole TUI. It now
runs on a background worker, matching the existing start/stop pattern.

completes plan item #5
EOF
)"
```
