# Lesson 20: Final integration

## Concept Intro

The final lesson ties everything together and makes the tool actually usable
day-to-day. Three things remain:

1. **Config-edit flow** — a `wt config` subcommand that opens (or prints) the
   TOML config so you can curate models and tags without hand-editing blind.
2. **Real integration check** — a smoke test against a real repo, real
   `ollama` (if installed), and a real agent launch, to confirm the whole
   pipeline works outside temp dirs.
3. **Docs** — update `README.md` and the `docs/` so the unified `wt` tool is
   the documented interface and the legacy shims are noted as forwards.

This is the point where you'd delete the old `wt-core.sh`-based wrappers'
logic — the shims from lesson 17 are now the only bash left, and they just
`exec wt`.

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

Wire it into `rootCmd`:

```go
cmd.AddCommand(modelsCmd(a), agentsCmd(a), configCmd(a))
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
wt --check-guard          # guard status
wt models --tag design    # non-interactive model list
wt --init                 # seed AGENTS.md in a scratch repo
wt -w smoke-test          # create worktree + launch (Ctrl-C to bail)
wt --cwd                  # launch in current dir
```

If `ollama` is installed, confirm discovered models appear in the browser:

```bash
wt models                 # should include 'local  discovered' entries
```

### Updating docs

Update `README.md` to lead with `wt`:

- Install: `go install ./cmd/wt` (or `make install` which also builds).
- Usage: the `wt` subcommands + flags table.
- Note the legacy `*-wt` shims forward to `wt --agent <name>`.

Update the `Makefile` `install` target to build the binary and copy the shims:

```makefile
install:
	go build -o $(HOME)/.local/bin/wt ./cmd/wt
	cp bin/*-wt $(HOME)/.local/bin/
	chmod +x $(HOME)/.local/bin/wt $(HOME)/.local/bin/*-wt
```

## Run It

```bash
go install ./cmd/wt
wt --help
wt config
EDITOR=nano wt config edit
wt models --tag code
```

## Try It Yourself

Add a `wt models add --id ... --provider ... --location ... --tag ...` command
that appends a curated entry to the config and re-saves it, so curation doesn't
require opening the file.

<details>
<summary>Solution</summary>

```go
// append a Model to a.cfg.Models, then config.Save(a.cfg)
a.cfg.Models = append(a.cfg.Models, config.Model{
	ID: id, Provider: provider, Location: config.Location(location),
	Tags: tags, Source: config.SourceCurated,
})
if err := config.Save(a.cfg); err != nil { return err }
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
