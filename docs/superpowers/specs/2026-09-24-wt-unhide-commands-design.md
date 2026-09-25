# Unhide all `wt` commands from help, with a regression guard

**Status:** approved design, ready for implementation
**Date:** 2026-09-24
**Branch:** `wt/unhide-commands` (worktree `.worktrees/wt-unhide-commands`)

## Problem

`wt --help` lists every command except `rotate <tag>`, which sets
`Hidden: true` (`wt/cmd/wt/commands.go:15`) — a debug helper that prints
the model after the last-launched one in a tag group. Every other command
added since (`config`, `litellm`, `profile`, `stats`, plus the older
`start`/`stop`/`smoke`) is visible, and `wt --help` and `wt __complete`
agree.

The real gap is coverage: `TestRootHelpListsModelSubcommands`
(`wt/cmd/wt/main_test.go:916`) only asserts that `start`, `stop`, and
`smoke` appear in help. A future command that is registered with `Hidden:
true`, or not registered at all, would silently drop out of help with no
test failure. There is also no invariant test preventing a command from
being hidden.

## Goal

No `wt` command is hidden from help, and a regression test enforces that
for the current command tree and every future addition.

## Non-goals

- The `--legacy-w` **flag** stays hidden (`wt/cmd/wt/main.go:436`,
  `MarkHidden`). It is not a command; it exists only to turn a removed
  `-w` invocation into a friendly "use `-W`" error, and revealing it in
  `--help` would advertise a flag users are meant to stop using.
- Cobra's own built-ins (`help`, `completion`, and the hidden
  `__complete`/`__completeNoDesc` plumbing) are untouched.
- No behavioural change to any command. `rotate` keeps its current
  implementation, arguments, and output; it only becomes listed.
- No change to the root help `Example` block. The commands already appear
  in `Available Commands`, which is the discoverability surface the goal
  names; example lines are a separate editorial choice.

## Design

### 1. Expose `rotate`

Remove the `Hidden: true` field from `rotateCmd` in
`wt/cmd/wt/commands.go`. Its `Use`, `Short`, and `RunE` are unchanged, so
it appears under `Available Commands` as:

```
rotate      Print the model after the last-launched in a tag group (debug)
```

The `(debug)` suffix in `Short` stays — it is accurate operator guidance,
not a reason to hide the command.

### 2. Structural invariant test

Add `TestNoWtCommandIsHidden` to `wt/cmd/wt/main_test.go`. It walks the
command tree returned by `rootCmd()` recursively and fails on any command
whose `Hidden` field is true, naming the offending `CommandPath()`.
Commands whose `Name()` starts with `__` are skipped: those are cobra's
own shell-completion plumbing, hidden by design and outside `wt`'s
control.

```go
func TestNoWtCommandIsHidden(t *testing.T) {
	var walk func(c *cobra.Command)
	walk = func(c *cobra.Command) {
		for _, child := range c.Commands() {
			if strings.HasPrefix(child.Name(), "__") {
				continue // cobra's hidden shell-completion plumbing
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

This is the invariant the goal states, enforced at the level where hiding
happens: if any present or future `wt` command sets `Hidden: true`, the
test fails.

### 3. Extend the help-text test

Extend `TestRootHelpListsModelSubcommands`'s expected list (and update its
doc comment) so `wt --help` is asserted to contain the newer top-level
commands as well:

```go
for _, want := range []string{
	"start", "stop", "smoke",
	"config", "litellm", "profile", "stats", "rotate",
} {
```

The existing example-fragment assertions (`wt start`, `wt stop`,
`wt smoke`) stay, so the test keeps covering both the command list and the
usage examples. The test keeps its name; a broader name is not required by
the goal.

The two tests are complementary: the structural test proves nothing is
hidden, and the help-text test proves the expected names are actually
rendered in the root help output (catching, e.g., a command removed from
`main.go`'s `AddCommand` call, which the structural walk alone would not
notice because an unregistered command is simply absent).

## Testing

- `cd wt && go test ./cmd/wt -run 'TestRootHelp|TestNoWtCommand' -v` —
  both tests pass; before the change, `TestNoWtCommandIsHidden` fails on
  `wt rotate` and the extended list fails on `rotate` (this is the red
  state that proves the tests are load-bearing).
- `cd wt && go build ./... && go vet ./...` — clean.
- Manual: `wt --help` now lists `rotate` among `Available Commands`;
  `wt rotate --help` is unchanged.

## Risks

- **Low.** `rotate` is already runnable (`wt rotate code` works today); it
  is only unlisted. No callers depend on help output.
- The structural walk calls `rootCmd()`, which calls `newApp()` and loads
  config/registry. The existing help test already does this and passes, so
  no new isolation requirements are introduced.
