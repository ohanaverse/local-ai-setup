# Lesson 20: Final integration

## Concept Intro

The Go tool is complete through lesson 19: `cmd/wt/` is split into
`main.go` (cobra wiring), `app.go` (shared dependencies), `commands.go`
(subcommands), `helpers.go` (accessors), and `launch.go` (non-TUI launch).
The TUI, model browser, rotation, guard, init seeding, and session resume
all work. The `bin/*-wt` shims forward to `wt --agent <name>`.

But the tool isn't fully usable day-to-day yet. Three things remain:

1. **Config-edit flow** — a `wt config` subcommand that opens (or prints)
   the TOML config so you can curate models and tags without hand-editing
   blind. Today there's no way to find or edit the config from the CLI.
2. **Real integration check** — a smoke test against a real repo, real
   `ollama` (if installed), and a real agent launch, to confirm the whole
   pipeline works outside temp dirs.
3. **Docs** — `README.md` still leads with the bash wrappers and the Go tool
   is described as "in progress." The `Makefile` `install` target only
   copies bash scripts — it doesn't build the `wt` binary. This lesson
   updates both so the unified `wt` tool is the documented interface and
   the legacy shims are noted as forwards.

> **`shell-wt` still needs bash.** The `*-wt` shims from lesson 17 are thin
> `exec wt` wrappers, but `shell-wt` still sources `wt-core.sh` (it launches
> a shell command, not an agent, and has no Go equivalent yet). So
> `wt-core.sh` and `wt-install-guard` must remain in `bin/` and stay
> co-located with the shims. This lesson does **not** delete them.

## New Syntax & Vocabulary

| Term | Meaning |
|---|---|
| `wt config` subcommand | Prints the config path; `wt config edit` opens it in `$EDITOR`. |
| `os.Getenv("EDITOR")` | Respect the user's editor. |
| `os/exec` + `cmd.Run()` | Launch the editor; revalidate config after edit. |
| smoke test | A manual/scripted end-to-end run, not a Go test. |
| `go install ./cmd/wt` | Install the binary to `GOBIN`/`GOPATH/bin`. |

## Worked Walkthrough

### `wt config` subcommand

Create `cmd/wt/config_cmd.go`:

```go
package main

import (
	"fmt"
	"os"
	"os/exec"

	"github.com/spf13/cobra"
	"github.com/ohanaverse/agent-worktree/internal/config"
)

func configCmd(a *app) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "config",
		Short: "Show or edit the wt config",
		Args:  cobra.MaximumNArgs(1), // optional "edit"
		RunE: func(cmd *cobra.Command, args []string) error {
			path := config.Path()
			if len(args) == 1 && args[0] == "edit" {
				return editConfig(path, a)
			}
			fmt.Println(path)
			return nil
		},
	}
	return cmd
}

func editConfig(path string, a *app) error {
	editor := os.Getenv("EDITOR")
	if editor == "" {
		editor = "vi"
	}
	c := exec.Command(editor, path)
	c.Stdin = os.Stdin
	c.Stdout = os.Stdout
	c.Stderr = os.Stderr
	if err := c.Run(); err != nil {
		return err
	}
	// Reload and revalidate so mistakes are caught immediately.
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	if err := cfg.ValidateAll(); err != nil {
		return fmt.Errorf("config has errors after edit:\n%w", err)
	}
	a.cfg = cfg
	fmt.Println("wt: config valid.")
	return nil
}
```

Wire it into `rootCmd`. The current line in `cmd/wt/main.go` is:

```go
cmd.AddCommand(modelsCmd(a), agentsCmd(a), rotateCmd(a))
```

Add `configCmd(a)`:

```go
cmd.AddCommand(modelsCmd(a), agentsCmd(a), rotateCmd(a), configCmd(a))
```

### Installing the binary

```bash
go install ./cmd/wt
```

This puts `wt` in your `GOBIN` (default `$GOPATH/bin`). Confirm it's on
`PATH`:

```bash
command -v wt
```

### Real smoke test

Run the full flow manually:

```bash
wt agents               # list registered agents + install status
wt models               # non-interactive model list (curated + discovered)
wt --init               # seed AGENTS.md in a scratch repo
wt -w smoke-test        # create worktree + launch (Ctrl-C to bail)
wt --cwd                # launch in current dir
```

If `ollama` is installed, confirm discovered models appear in the model
list:

```bash
wt models               # should include 'discovered' source entries
```

### Updating docs

**`README.md`** currently leads with the bash wrappers and describes the Go
tool as "in progress." Update it to lead with `wt`:

- Install: `go install ./cmd/wt` (or `make install` which also builds —
  see below).
- Usage: the `wt` subcommands + flags table (already documented in
  `CLAUDE.md`).
- Note the legacy `*-wt` shims forward to `wt --agent <name>`.
- Keep the `shell-wt` row since it still needs `wt-core.sh`.

**`Makefile`** — the current `install` target only copies bash scripts:

```makefile
install:                # Install scripts to ~/.local/bin/
	cp -r $(SRCDIR)/* $(BINDIR)/
	chmod +x $(BINDIR)/*-wt $(BINDIR)/wt-core.sh $(BINDIR)/wt-install-guard
```

Update it to also build the `wt` binary, while still copying the bash
scripts that `shell-wt` needs:

```makefile
install:                # Install wt binary + scripts to ~/.local/bin/
	go build -o $(BINDIR)/wt ./cmd/wt
	cp -r $(SRCDIR)/* $(BINDIR)/
	chmod +x $(BINDIR)/wt $(BINDIR)/*-wt $(BINDIR)/wt-core.sh $(BINDIR)/wt-install-guard
```

Also add `$(BINDIR)/wt` to the `uninstall` target's `rm -f` list.

## Run It

```bash
go install ./cmd/wt
wt --help
wt config
EDITOR=nano wt config edit
wt models
```

## Try It Yourself

Add a `wt models add --id ... --family ... --provider ... --model-name ...
--location ... --tag ...` command that appends a curated entry to the
config and re-saves it, so curation doesn't require opening the file.

<details>
<summary>Solution</summary>

```go
// append a Model to a.cfg.Models, then config.Save(a.cfg)
a.cfg.Models = append(a.cfg.Models, config.Model{
	ID:         id,
	Family:     family,
	ProviderID: provider,
	ModelName:  modelName,
	Location:   config.Location(location),
	Tags:       tags,
	Source:     config.SourceCurated,
})
if err := config.Save(a.cfg); err != nil {
	return err
}
```
</details>

## Checkpoint

```bash
git add -A && git commit -m "lesson 20: final integration" && git tag lesson-20
```

---

**Course complete.** You've rebuilt the `*-wt` launchers as a single, unified,
tested Go TUI tool: a hybrid model registry (curated + discovered), tag-based
rotation, a full model browser, worktree/branch picking, the main guard,
`--init` seeding, and session resume — all behind one `wt` binary with the old
`*-wt` commands as thin shims.
