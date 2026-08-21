# `wt config`

User-level preferences for the `wt` launcher. The first shipped surface is
color themes; future subcommands (ollama sync, registry editing, etc.) slot
in here without breaking changes.

## Where settings live

All `wt config` settings live in `~/.config/agent-wt/` (or
`$XDG_CONFIG_HOME/agent-wt/`). The theme subcommand writes
`themes.toml`; other subcommands will add their own files alongside it.

```
~/.config/agent-wt/
├── config.toml          # main agent / model registry (existing)
├── themes.toml          # active theme (this feature)
├── rotation-*.state     # per-slot last-launched model
└── ...                  # future subcommands land here
```

Print the resolved config directory at any time:

```bash
wt config path
```

## Themes

The `wt config theme` family manages the active color theme. Themes apply
to the TUI picker (worktree list, agent list, model list), CLI tables
(`wt models`, `wt agents`), and the `wt config theme list` output.

### Built-in themes

| Name           | Description                          |
|----------------|--------------------------------------|
| `default`      | Subtle, terminal-native colors       |
| `solarized`    | Warm palette inspired by Solarized   |
| `mono`         | Grayscale with a single blue accent  |
| `tokyo-night`  | Enkia's Tokyo Night (Night/Day pair) |

Each theme carries dark and light variants of every token, so the same
choice works in either terminal mode without flipping a setting.

### Commands

```bash
wt config theme               # show the active theme + available names
wt config theme list          # list all built-in themes (table)
wt config theme show          # show the active theme's tokens
wt config theme show <name>   # show a specific theme's tokens
wt config theme set <name>    # activate a theme (effective on next wt launch)
wt config theme unset         # revert to the default theme
```

`show` output (the `dark` and `light` words render in the token's own
dark/light color so the hex values stay readable):

```
solarized — warm palette inspired by Solarized

  border       #93a1a1 / #93a1a1  dark light
  error        #dc322f / #dc322f  dark light
  header       #b58900 / #b58900  dark light
  ...
```

### The themes.toml file

`wt config theme set <name>` writes a single line to
`~/.config/agent-wt/themes.toml`:

```toml
theme = "tokyo-night"
```

The file is read once at startup (in `newApp()`); the active theme does
**not** hot-reload — relaunch `wt` to see changes. A missing or empty
file falls back to the `default` theme. Unknown theme names fall back
to `default` as well, so a typo never crashes the launcher.

### Adding custom themes (not yet supported)

The first release ships four built-in palettes only. A future release
will let users define themes in `themes.toml` (or a separate file) so
custom palettes can be versioned alongside `config.toml`.