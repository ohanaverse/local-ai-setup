# Lesson 1: Module init & CLI skeleton

## Concept Intro

The `*-wt` launchers are bash scripts. We're rebuilding them as a single Go
binary called `wt`. The first thing to set up is the Go module and a CLI with
subcommands, because the unified tool needs to dispatch different behaviors:
`wt` (interactive TUI), `wt models` (manage the model registry), `wt agents`
(list/set default agents), plus flags like `--init`, `--no-guard`,
`--check-guard`, `-w`, `--cwd`, `--yolo`.

In Go, the idiomatic way to build a CLI with subcommands is the **cobra**
library (the standard `flag` package only handles a flat set of flags on one
command). Cobra gives you nested commands, help generation, and flag
validation for free. This is the analog of the bash wrapper's
`parse_wt_args` flag parsing in `wt-core.sh`, but structured as a command
tree instead of a `case` statement.

You're already comfortable with Go, so this lesson is mostly about the cobra
pattern: defining commands as structs, wiring them under a root command, and
structuring a small program so the real work lives in functions you can call
from elsewhere (and test) rather than directly in `main()`.

## New Syntax & Vocabulary

| Term | Meaning |
|---|---|
| `go mod init <module>` | Creates a `go.mod` with the module path (analog of `cargo init`). |
| `cobra.Command` | A single command in the tree; has `Use`, `Short`, `Run`/`RunE`, `Args`. |
| `command.AddCommand(...)` | Registers child (sub)commands under a parent. |
| `PersistentFlags()` | Flags inherited by the command *and* all its subcommands. |
| `command.Flags()` | Flags local to one command only. |
| `RunE` vs `Run` | `RunE` returns an `error` so you can bubble it up and set the exit code centrally. |
| `SilenceUsage` | Tells cobra not to print usage on a returned error. |
| `os.Exit(1)` | Terminate with non-zero status after printing an error (analog of `die`). |

## Worked Walkthrough

Start by creating the module at the repo root:

```bash
go mod init github.com/ohanaverse/agent-worktree
```

This creates `go.mod` (and `go.sum` on first build). The existing bash
wrappers in `bin/` are untouched for now.

### Adding cobra

```bash
go get github.com/spf13/cobra@latest
```

### The root command

Create `cmd/wt/main.go`:

```go
package main

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

func main() {
	if err := rootCmd().Execute(); err != nil {
		fmt.Fprintln(os.Stderr, "wt:", err)
		os.Exit(1)
	}
}

func rootCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "wt",
		Short: "Launch AI coding agents in a chosen worktree, branch, and model",
		// With no subcommand, wt launches the interactive TUI.
		RunE: func(cmd *cobra.Command, args []string) error {
			fmt.Println("(TUI not yet implemented — coming in lesson 12)")
			return nil
		},
	}

	// Flags shared by wt and its subcommands.
	cmd.PersistentFlags().Bool("yolo", false, "Skip permission prompts")
	cmd.PersistentFlags().StringP("worktree", "w", "", "Use/create worktree for branch")

	cmd.AddCommand(modelsCmd(), agentsCmd())
	return cmd
}

func modelsCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "models",
		Short: "Browse and manage the model registry",
		Run: func(cmd *cobra.Command, args []string) {
			fmt.Println("(models registry not yet implemented — lesson 2)")
		},
	}
}

func agentsCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "agents",
		Short: "List installed agents and set defaults",
		Run: func(cmd *cobra.Command, args []string) {
			fmt.Println("(agents not yet implemented — lesson 6)")
		},
	}
}
```

The `PersistentFlags` on the root are inherited by subcommands, so
`wt models --yolo` parses too (harmless here; we'll use `--yolo` in the
launch flow later).

### Running with an exit code

Notice the `main()` pattern: `Execute()` returns an `error` which we print to
stderr and turn into `os.Exit(1)`. This centralizes exit-code handling instead
of scattering `os.Exit` calls — the Go analog of `wt-core.sh`'s `die()`.

## Run It

```bash
go run ./cmd/wt --help
```

```
Launch AI coding agents in a chosen worktree, branch, and model

Usage:
  wt [flags]
  wt [command]

Available Commands:
  agents      List installed agents and set defaults
  completion  Generate the autocompletion script for the specified shell
  help        Help about any command
  models      Browse and manage the model registry

Flags:
  -h, --help              help for wt
  -w, --worktree string   Use/create worktree for branch
      --yolo              Skip permission prompts
```

```bash
go run ./cmd/wt
```

```
(TUI not yet implemented — coming in lesson 12)
```

```bash
go run ./cmd/wt models
```

```
(models registry not yet implemented — lesson 2)
```

## Try It Yourself

Add a hidden `version` flag (persistent, bool) to the root command that, when
set, prints a version string and exits 0 without running the TUI.

<details>
<summary>Solution</summary>

Add to `rootCmd()`:

```go
var version bool
cmd.PersistentFlags().BoolVar(&version, "version", false, "Print version and exit")
cmd.RunE = func(cmd *cobra.Command, args []string) error {
	if version {
		fmt.Println("wt 0.1.0")
		return nil
	}
	fmt.Println("(TUI not yet implemented — coming in lesson 12)")
	return nil
}
```

Now `wt --version` prints `wt 0.1.0` instead of the placeholder.
</details>

## Checkpoint

```bash
git add -A && git commit -m "lesson 01: module init & CLI skeleton" && git tag lesson-01
```
