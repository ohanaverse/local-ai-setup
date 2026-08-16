# Lesson 19: Module split & polish

## Concept Intro

The code works, but `cmd/wt/main.go` has grown to 329 lines: every flag, the
whole launch flow, and the `models`/`agents` subcommands all live in one file,
while the shared helpers (`mustGetString`, `yolo`, `defaultModel`,
`inGitRepo`) sit in `launch.go`. This lesson is a **pure refactor** (no new
features): split the CLI into focused files, centralize error handling,
improve help text, and make the flag surface clean and consistent. This
mirrors how `wt-core.sh` grew a shared contract; in Go we express that
contract as package boundaries and a single entry point.

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

Keep `cmd/wt/main.go` minimal — just cobra wiring and exit-code handling:

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

Today each subcommand calls `config.Load()` itself (see `modelsCmd` in
`main.go`). Instead, build a small `app` struct holding shared dependencies
and load them once:

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

> **One intentional behavior change.** The current `wt models` command lists
> only curated `cfg.Models`. By wiring `registry.Discover(cfg)` into `app`,
> the CLI now surfaces discovered models too — matching the TUI model browser
> from lesson 15. Everything else in this lesson is behavior-preserving.

Then command constructors take `*app` instead of loading config themselves.
The `models` command keeps its three-table rendering but reads from `a.cfg`
and `a.models`:

```go
// cmd/wt/commands.go
func modelsCmd(a *app) *cobra.Command {
	return &cobra.Command{
		Use:     "models",
		Aliases: []string{"model"},
		Short:   "Browse and manage the model registry",
		Example: "  wt models          # list the registry\n  wt models --tag code",
		RunE: func(cmd *cobra.Command, args []string) error {
			// Providers table
			provRows := make([][]string, 0, len(a.cfg.Providers))
			for _, p := range a.cfg.Providers {
				provRows = append(provRows, []string{
					p.ID, string(p.Location), p.Auth.Type, p.Auth.BaseURL,
				})
			}
			fmt.Println("Providers:")
			fmt.Println(renderTable([]string{"ID", "LOCATION", "AUTH", "BASE_URL"}, provRows))

			// Models table — sort by provider, then ID
			models := make([]config.Model, len(a.models))
			copy(models, a.models)
			sort.Slice(models, func(i, j int) bool {
				if models[i].ProviderID != models[j].ProviderID {
					return models[i].ProviderID < models[j].ProviderID
				}
				return models[i].ID < models[j].ID
			})
			modelRows := make([][]string, 0, len(models))
			for _, m := range models {
				loc, _ := a.cfg.ResolveLocation(m)
				modelRows = append(modelRows, []string{
					m.ID, m.Family, m.ProviderID, string(loc), strings.Join(m.Tags, ", "),
				})
			}
			fmt.Println("Models:")
			fmt.Println(renderTable([]string{"ID", "FAMILY", "PROVIDER", "LOCATION", "TAGS"}, modelRows))

			// Agents table
			agentRows := make([][]string, 0, len(a.cfg.Agents))
			for _, ag := range a.cfg.Agents {
				agentRows = append(agentRows, []string{
					ag.Name, strings.Join(ag.SupportedProviders, ", "), ag.DefaultProvider,
				})
			}
			fmt.Println("Agents:")
			fmt.Println(renderTable([]string{"NAME", "PROVIDERS", "DEFAULT"}, agentRows))
			return nil
		},
	}
}
```

`agentsCmd` gets the same treatment — it no longer needs `config.Load()`
because it only reads the agent registry, but it still takes `*app` for a
consistent constructor signature:

```go
func agentsCmd(a *app) *cobra.Command {
	return &cobra.Command{
		Use:     "agents",
		Short:   "List installed agents and set defaults",
		Example: "  wt agents",
		Run: func(cmd *cobra.Command, args []string) {
			names := agents.Names()
			sort.Strings(names)
			rows := make([][]string, 0, len(names))
			for _, n := range names {
				d := agents.ByName(n)
				installed := "no"
				if agents.Installed(n) {
					installed = "yes"
				}
				rows = append(rows, []string{n, installed, d.YoloFlag()})
			}
			fmt.Println("Agents:")
			fmt.Println(renderTable([]string{"NAME", "INSTALLED", "YOLO_FLAG"}, rows))
		},
	}
}
```

Wire them in `rootCmd`:

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

Move `mustGetString`, `yolo`, `defaultAgent`, `defaultModel`, `inGitRepo`,
`inGitRepoAt`, and `renderTable` into `cmd/wt/helpers.go` so every command
uses the same accessors. These already exist in `launch.go`/`main.go`; the
refactor just relocates them and gives them doc comments:

```go
// cmd/wt/helpers.go
package main

import (
	"os/exec"

	"github.com/ohanaverse/agent-worktree/internal/config"
	"github.com/spf13/cobra"
)

// mustGetString returns a string flag value, ignoring the error. The flag is
// always registered, so GetString cannot fail here.
func mustGetString(cmd *cobra.Command, name string) string {
	v, _ := cmd.Flags().GetString(name)
	return v
}

// yolo reports whether the --yolo flag was set.
func yolo(cmd *cobra.Command) bool {
	v, _ := cmd.Flags().GetBool("yolo")
	return v
}

// inGitRepo reports whether the current directory is inside a git repo.
func inGitRepo() bool {
	return inGitRepoAt(".")
}

// inGitRepoAt reports whether dir is inside a git repo. Separated from
// inGitRepo so tests can point it at a temp repo without chdir'ing the
// process.
func inGitRepoAt(dir string) bool {
	return exec.Command("git", "-C", dir, "rev-parse", "--git-dir").Run() == nil
}

// defaultAgent returns the agent to launch when --agent is not given: the
// first configured agent, falling back to "claude".
func defaultAgent(cfg *config.Config) string {
	if cfg != nil && len(cfg.Agents) > 0 {
		return cfg.Agents[0].Name
	}
	return "claude"
}

// defaultModel returns the model to launch for an agent: the agent's native
// model (e.g. claude/native) if present, else the first model in the default
// tag group.
func defaultModel(cfg *config.Config, agent string) config.Model {
	for _, m := range cfg.Models {
		if m.ID == agent+"/native" {
			return m
		}
	}
	ms := cfg.ModelsWithTag(cfg.DefaultTag)
	if len(ms) > 0 {
		return ms[0]
	}
	return config.Model{ID: "(none)", Location: config.LocationCloud}
}
```

`renderTable` and `borderStyle` move here too, so `commands.go` and the launch
path share one table renderer.

**Optional polish — surface all config problems at once.** The current
`config.Validate()` returns the *first* error. If you want to report every
problem in one pass, add a `ValidateAll` method that collects errors and
joins them with `errors.Join`:

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
go run ./cmd/wt models
go vet ./...
gofmt -l cmd internal   # should print nothing
```

## Try It Yourself

Move the `--rotate-tag` debug flag out of the root command into a proper
hidden subcommand `wt rotate <tag>` so the root command's `RunE` only handles
the real launch flow. Today `--rotate-tag` is a root flag handled inline in
`rootCmd().RunE`; pull it out into a constructor that takes `*app` and uses
`rotation.ForTag`:

<details>
<summary>Solution</summary>

```go
// cmd/wt/commands.go
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

Register it in `rootCmd` with `cmd.AddCommand(modelsCmd(a), agentsCmd(a),
rotateCmd(a))` and delete the `--rotate-tag` flag plus its inline handler.
Verify with:

```bash
go run ./cmd/wt rotate code
go run ./cmd/wt rotate design
```
</details>

## Checkpoint

```bash
git add -A && git commit -m "lesson 19: module split & polish" && git tag lesson-19
```
