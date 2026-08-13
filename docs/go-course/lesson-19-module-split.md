# Lesson 19: Module split & polish

## Concept Intro

The code works, but the CLI has accumulated ad-hoc flags and helpers. This
lesson is a **pure refactor** (no new features): tighten the module layout,
centralize error handling, improve help text, and make the flag surface clean
and consistent. This mirrors how `wt-core.sh` grew a shared contract; in Go we
express that contract as package boundaries and a single entry point.

Goals:
- `main` stays thin — only cobra wiring and exit-code handling.
- Flag parsing lives in one place (`cmd/wt`), not scattered.
- Shared helpers (`mustGetString`, `yolo`, `defaultModel`) are consistent.
- Every subcommand has good `Short`/`Long`/`Example` help text.
- `go vet` and `gofmt` are clean.

## New Syntax & Vocabulary

| Term | Meaning |
|---|---|
| `internal/` packages | Enforced by the Go toolchain — only importable from within the module. |
| command constructors | Each subcommand gets its own `func newXCmd(deps) *cobra.Command`. |
| dependency injection | Pass `cfg`/`agent registry` into command constructors instead of globals. |
| `gofmt` / `goimports` | Formatting gates. |
| `errors.Join` | Combine multiple validation errors (Go 1.20+). |

## Worked Walkthrough

### Thin main

Keep `cmd/wt/main.go` minimal:

```go
package main

import (
	"fmt"
	"os"
)

func main() {
	if err := rootCmd().Execute(); err != nil {
		fmt.Fprintln(os.Stderr, "wt:", err)
		os.Exit(1)
	}
}
```

Move all the command construction and shared helpers into
`cmd/wt/commands.go` and `cmd/wt/helpers.go`.

### Dependency injection for commands

Instead of each subcommand calling `config.Load()` itself, build a small
`app` struct holding shared dependencies:

```go
// cmd/wt/app.go
package main

import (
	"github.com/ohanaverse/agent-worktree/internal/config"
	"github.com/ohanaverse/agent-worktree/internal/registry"
)

type app struct {
	cfg    *config.Config
	models []config.Model // curated + discovered registry
}

func newApp() (*app, error) {
	cfg, err := config.Load()
	if err != nil {
		return nil, err
	}
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return &app{cfg: cfg, models: registry.Discover(cfg)}, nil
}
```

Then command constructors take `*app`:

```go
func modelsCmd(a *app) *cobra.Command {
	return &cobra.Command{
		Use:     "models",
		Aliases: []string{"model"},
		Short:   "Browse and manage the model registry",
		Example: "  wt models          # interactive browser\n  wt models --tag code",
		RunE: func(cmd *cobra.Command, args []string) error {
			tag, _ := cmd.Flags().GetString("tag")
			for _, m := range registry.FilterByTag(a.models, tag) {
				fmt.Printf("%-24s %-10s %-6s %v\n", m.ID, m.Provider, m.Location, m.Tags)
			}
			return nil
		},
	}
}
```

Wire it in `rootCmd`:

```go
func rootCmd() *cobra.Command {
	a, err := newApp()
	if err != nil {
		fmt.Fprintln(os.Stderr, "wt: config error:", err)
		os.Exit(1)
	}
	cmd := &cobra.Command{ ... }
	cmd.AddCommand(modelsCmd(a), agentsCmd(a))
	return cmd
}
```

### Centralized helpers

Move `mustGetString`, `yolo`, `defaultModel`, `inGitRepo` into
`cmd/wt/helpers.go` so every command uses the same accessors. Use `errors.Join`
to surface all config problems at once:

```go
func (c *Config) ValidateAll() error {
	var errs []error
	if c.DefaultTag == "" {
		errs = append(errs, errors.New("default_tag must not be empty"))
	}
	seen := map[string]bool{}
	for _, m := range c.Models {
		if m.ID == "" {
			errs = append(errs, errors.New("model with empty id"))
		}
		if seen[m.ID] {
			errs = append(errs, fmt.Errorf("duplicate model id %q", m.ID))
		}
		seen[m.ID] = true
	}
	return errors.Join(errs...)
}
```

### Help text polish

Give every command `Example` and a consistent description verb ("Launch…",
"Browse…", "List…"). Run:

```bash
gofmt -w cmd internal
go vet ./...
```

## Run It

```bash
go run ./cmd/wt --help
go run ./cmd/wt models --help
go run ./cmd/wt models --tag design
go vet ./...
gofmt -l cmd internal   # should print nothing
```

## Try It Yourself

Move the `--rotate-tag` debug flag out of the root command into a proper
hidden subcommand `wt rotate <tag>` so the root command's `RunE` only handles
the real launch flow.

<details>
<summary>Solution</summary>

```go
func rotateCmd(a *app) *cobra.Command {
	var crossTag string
	c := &cobra.Command{
		Use:    "rotate <tag>",
		Short:  "Print the next model in a tag group's rotation (debug)",
		Hidden: true,
		Args:   cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			r := rotation.ForTag(a.cfg, args[0])
			m, ok := r.Next(crossTag)
			if !ok {
				return fmt.Errorf("no models tagged %q", args[0])
			}
			fmt.Println(m.ID)
			return nil
		},
	}
	c.Flags().StringVar(&crossTag, "cross-tag", "", "skip models used by this tag")
	return c
}
```
</details>

## Checkpoint

```bash
git add -A && git commit -m "lesson 19: module split & polish" && git tag lesson-19
```
