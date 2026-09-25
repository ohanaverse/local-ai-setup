# Unhide all `wt` commands from help Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make `wt --help` list every `wt` command (exposing `rotate`) and add a regression test that fails if any command is ever hidden.

**Architecture:** Two edits to `wt/cmd/wt`: drop `Hidden: true` from `rotateCmd`, and add/extend tests in `main_test.go`. TDD order: the new structural test and the widened help-text list both fail before the one-line production change, then pass after it. No command behavior changes.

**Tech Stack:** Go, `github.com/spf13/cobra`, `go test`.

**Spec:** `docs/superpowers/specs/2026-09-24-wt-unhide-commands-design.md`

---

## File structure

- **Modify:** `wt/cmd/wt/commands.go` — remove the `Hidden: true` field from `rotateCmd` (one line).
- **Modify:** `wt/cmd/wt/main_test.go` — add `TestNoWtCommandIsHidden` and widen `TestRootHelpListsModelSubcommands`'s expected list. `cobra` and `strings` are already imported, so no import changes.

Work from: `/Users/keith/github/ohanaverse/local-ai-setup/.worktrees/wt-unhide-commands`

---

### Task 1: Unhide `rotate` and guard against hidden commands

**Files:**
- Modify: `wt/cmd/wt/main_test.go:916-935` (the existing help test) and append the new test after it
- Modify: `wt/cmd/wt/commands.go:11-17`

- [ ] **Step 1: Add the failing structural test**

In `wt/cmd/wt/main_test.go`, immediately after the closing brace of `TestRootHelpListsModelSubcommands` (currently around line 935), add:

```go
// TestNoWtCommandIsHidden asserts no wt command is hidden from `wt --help`.
// A hidden command is undiscoverable — absent from both help and shell
// completion — so this guards every present and future command against an
// accidental Hidden: true. Cobra's own "__"-prefixed shell-completion
// plumbing is hidden by design and skipped.
func TestNoWtCommandIsHidden(t *testing.T) {
	var walk func(c *cobra.Command)
	walk = func(c *cobra.Command) {
		for _, child := range c.Commands() {
			if strings.HasPrefix(child.Name(), "__") {
				continue
			}
			if child.Hidden {
				t.Errorf("command %q is hidden from help", child.CommandPath())
			}
			walk(child)
		}
	}
	walk(rootCmd())
}
```

- [ ] **Step 2: Widen the existing help-text test's expected list**

In `TestRootHelpListsModelSubcommands` (`wt/cmd/wt/main_test.go`), make
two exact replacements. First, replace the doc comment:

```go
// TestRootHelpListsModelSubcommands verifies `wt --help` documents start, stop
// and smoke with usage examples, so the model subcommands are discoverable
// without reading the docs.
```

with:

```go
// TestRootHelpListsModelSubcommands verifies `wt --help` documents the
// model subcommands and the newer top-level commands, with the model
// subcommands' usage examples, so the command set is discoverable without
// reading the docs.
```

Second, replace:

```go
	for _, want := range []string{"start", "stop", "smoke", "wt start", "wt stop", "wt smoke"} {
```

with:

```go
	for _, want := range []string{
		"start", "stop", "smoke",
		"config", "litellm", "profile", "stats", "rotate",
		"wt start", "wt stop", "wt smoke",
	} {
```

Change nothing else in the function.

- [ ] **Step 3: Run both tests and verify they FAIL (the red state)**

Run:
```bash
cd wt && go test ./cmd/wt -run 'TestRootHelp|TestNoWtCommand' -v
```
Expected: both fail. `TestNoWtCommandIsHidden` reports:
```
command "wt rotate" is hidden from help
```
`TestRootHelpListsModelSubcommands` reports help output missing `"rotate"` (and the other newly listed names if any are absent). This proves the tests are load-bearing: they fail for the exact reason the change fixes.

- [ ] **Step 4: Remove `Hidden: true` from `rotateCmd`**

In `wt/cmd/wt/commands.go`, delete the `Hidden: true,` line so the command literal reads:

```go
	c := &cobra.Command{
		Use:   "rotate <tag>",
		Short: "Print the model after the last-launched in a tag group (debug)",
		Args:  cobra.ExactArgs(1),
```

Change nothing else — not `Use`, `Short`, `Args`, or `RunE`.

- [ ] **Step 5: Run the tests and verify they PASS**

Run:
```bash
cd wt && go test ./cmd/wt -run 'TestRootHelp|TestNoWtCommand' -v
```
Expected: `PASS` for both, and the `-v` output lists `TestNoWtCommandIsHidden` and `TestRootHelpListsModelSubcommands`.

- [ ] **Step 6: Format, build, vet**

Run:
```bash
cd wt && gofmt -l cmd/wt/commands.go cmd/wt/main_test.go && go build ./... && go vet ./...
```
Expected: `gofmt -l` prints nothing (both files formatted), and `go build`/`go vet` exit 0 with no output.

- [ ] **Step 7: Run the full package suite**

Run:
```bash
cd wt && go test ./cmd/wt
```
Expected: `ok  github.com/ohanaverse/local-ai-setup/wt/cmd/wt`. If anything unrelated is already broken, stop and report it rather than proceeding.

- [ ] **Step 8: Manual confirmation**

Run:
```bash
cd wt && go run ./cmd/wt --help | sed -n '/Available Commands:/,/^$/p'
```
Expected output includes a `rotate` line:
```
  rotate      Print the model after the last-launched in a tag group (debug)
```
and still lists `config`, `litellm`, `profile`, `stats`, `start`, `stop`, `smoke`.

- [ ] **Step 9: Commit**

```bash
git add wt/cmd/wt/commands.go wt/cmd/wt/main_test.go
git commit -m "wt: unhide rotate and guard against hidden commands

wt rotate was the only command with Hidden: true, so it never appeared in
wt --help or shell completion. Expose it and add TestNoWtCommandIsHidden,
which walks the command tree and fails if any wt command is hidden again;
also widen the root-help text test to assert the newer top-level commands
(config, litellm, profile, stats, rotate) are listed."
```

---

## Self-review

- **Spec coverage:** spec §1 (expose `rotate`) → Step 4; §2 (structural invariant test) → Step 1; §3 (extend help-text test) → Step 2; spec's Testing section → Steps 3, 5, 6, 7, 8; spec's "before the change ... fails" red-state claim → Step 3. Non-goals (hidden `--legacy-w` flag, cobra built-ins, `Example` block) → no task touches them.
- **Placeholders:** none; every code step shows the exact edit and every command its expected output.
- **Consistency:** the test names, the `want` slice contents, and the expected `rotate` help line match the spec verbatim; `cobra`/`strings` are confirmed already imported in `main_test.go`.
