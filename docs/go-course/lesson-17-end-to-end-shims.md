# Lesson 17: End-to-end flow + legacy shims

## Concept Intro

By now the pieces exist but the app still has gaps: the non-interactive flags
(`-w <name>`, `--cwd`) don't yet bypass the picker, session resume isn't wired
into launch, and the old `*-wt` binaries still point at bash. This lesson
completes the flow:

1. **Non-interactive paths** — `-w <name>` and `--cwd` skip the TUI entirely
   and go straight to `worktree.EnsureForName` / the cwd, then launch.
2. **Outside a git repo** — pass through to the agent with no picker.
3. **Session resume wiring** — the challenge from lesson 16, now integrated.
4. **Legacy shims** — replace the bash wrappers with tiny `exec wt` shims so
   `claude-wt -w foo` becomes `wt -w foo --agent claude`.

We also add an `--agent <name>` flag so a shim can pin which agent to launch,
and make the default agent come from the `agent_defaults` config (falling back
to a hard-coded list).

## New Syntax & Vocabulary

| Term | Meaning |
|---|---|
| `--agent <name>` flag | Pins the agent, used by legacy shims. |
| passthrough | Running the agent with no picker when outside a repo. |
| `os.Exit(cmd.Run() error)` | Propagate the agent's exit code so scripts see it. |
| shim script | `exec wt -w "$1" --agent claude "$@"` — a one-liner. |
| exit code preservation | The agent's status must reach the shell. |

## Worked Walkthrough

### Non-interactive paths in `rootCmd`

In the root `RunE`, restructure so the interactive TUI is only the fallback.
The order mirrors `wt_main` in `wt-core.sh`:

```go
func rootRun(cmd *cobra.Command, args []string) error {
	// ... guard, init, rotate-tag handling from earlier lessons ...

	// Resolve agent (from flag or config default).
	agent := mustGetString(cmd, "agent")
	if agent == "" {
		agent = defaultAgent(cfg)
	}

	// -w <name>: use/create a worktree, then launch (no picker).
	if name := mustGetString(cmd, "worktree"); name != "" {
		root, err := worktree.RepoRoot()
		if err != nil {
			return err
		}
		path, err := worktree.EnsureForName(name)
		if err != nil {
			return err
		}
		return launch(root, agent, path, cfg, yolo(cmd))
	}

	// --cwd: launch in the current repo root.
	if cwd, _ := cmd.Flags().GetBool("cwd"); cwd {
		root, err := worktree.RepoRoot()
		if err != nil {
			return err
		}
		return launch(root, agent, root, cfg, yolo(cmd))
	}

	// Outside a git repo: pure passthrough to the agent.
	if !inGitRepo() {
		return launchDirect(agent, cfg, yolo(cmd))
	}

	// Interactive TUI.
	return tui.Run()
}
```

### The launch helper (non-TUI)

A `launch` helper builds and runs the agent command directly (no TUI):

```go
func launch(repoRoot, agent, worktreePath string, cfg *config.Config, yolo bool) error {
	m := defaultModel(cfg, agent)
	d := agents.ByName(agent)
	cmd, err := agents.Command(d, m, yolo, worktreePath)
	if err != nil {
		return err
	}
	// session resume
	if s, _ := session.LatestForAgent(agent, worktreePath); s != nil {
		switch agent {
		case "claude":
			cmd.Args = append(cmd.Args, "--resume", s.ID)
		case "opencode":
			cmd.Args = append(cmd.Args, "--session", s.ID)
		}
	}
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	err = cmd.Run()
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			os.Exit(ee.ExitCode()) // preserve agent's exit code
		}
		return err
	}
	return nil
}
```

`defaultModel` returns the agent's default model from `agent_defaults`, falling
back to the first `code`-tagged model. `inGitRepo` uses
`git rev-parse --git-dir`. `yolo` reads the `--yolo` flag.

### Wiring the new flags

Add `--agent` and `--cwd` to the root command:

```go
cmd.PersistentFlags().String("agent", "", "Agent to launch (claude, codex, ...)")
cmd.PersistentFlags().Bool("cwd", false, "Launch in the current repo root, no picker")
```

### Legacy shims

Replace each `*-wt` bash wrapper with a one-liner. `bin/claude-wt`:

```sh
#!/usr/bin/env bash
# legacy shim — forwards to the unified wt tool.
exec wt -w "$@" --agent claude
```

Wait — that's wrong: `-w` consumes a value. The shim should forward the
original args and add `--agent`:

```sh
#!/usr/bin/env bash
# legacy shim — forwards to the unified wt tool.
exec wt --agent claude "$@"
```

Now `claude-wt -w foo` becomes `wt --agent claude -w foo`. Do the same for
`codex-wt`, `copilot-wt`, `pi-wt`, `agy-wt`, `opencode-wt`, and `shell-wt`
(which sets no agent — it launches a shell).

## Run It

```bash
go run ./cmd/wt --agent claude -w my-feature
```

Creates/uses the worktree and launches Claude there, no picker.

```bash
go run ./cmd/wt --agent codex --cwd
```

Launches codex in the current repo root.

Install the binary, then test a shim:

```bash
go build -o "$(go env GOPATH)/bin/wt" ./cmd/wt
claude-wt --cwd   # forwards to wt --agent claude --cwd
```

## Try It Yourself

Verify exit-code preservation: run `wt --agent codex --cwd` where codex exits
with a non-zero code, and confirm the shell's `$?` reflects it.

<details>
<summary>Solution</summary>

The `launch` helper already calls `os.Exit(ee.ExitCode())` on an `exec.ExitError`.
Test with a fake: temporarily point the agent at a script that `exit 3`, run,
and assert `echo $?` prints `3`.
</details>

## Checkpoint

```bash
git add -A && git commit -m "lesson 17: end-to-end flow + legacy shims" && git tag lesson-17
```
