# `wt config` and color themes — design

**Date:** 2026-08-20
**Status:** Draft (pending user review)

## Summary

Add a `wt config` command for managing user-level preferences, with color themes as the first shipped feature. Themes are 4 built-in palettes (default, solarized, mono, tokyo-night) with automatic light/dark adaptation via `lipgloss.AdaptiveColor`. The active theme is stored in `~/.config/agent-wt/themes.toml`; theme definitions are compiled into the binary.

## Goals

- Users can set a color theme that applies to all `wt` output (TUI + CLI tables).
- Themes work correctly on both light- and dark-background terminals.
- `NO_COLOR` is honored (lipgloss default; no extra code).
- The `wt config` command structure is ready to host future subcommands (`wt config ollama sync`, `wt config registry edit`) without breaking changes.

## Non-goals

- User-customizable palette (per-token override). Themes are authored in code only.
- Theme hot-reload. Re-launch `wt` to see changes.
- Per-command theme overrides.
- User-authored theme files. Themes are compiled in.
- Per-agent or per-provider theming.

## Background

`wt` currently styles 3 surfaces with hardcoded lipgloss values:

- `cmd/wt/helpers.go:40` — `borderStyle` (ANSI 240) for table borders in `wt models` and `wt agents`.
- `internal/tui/new_worktree.go:20` — `errorStyle` (ANSI 9, bright red) for error text.
- `internal/tui/model_list.go:82` — padding style (no color) wrapping the model picker view.

Plus `bubbles/list` defaults for selected/unselected rows in the pickers. The codebase has no other inline color usage — themes can be applied with minimal surface area.

`wt` already has a `~/.config/agent-wt/` directory pattern with one registry file (`config.toml`) and per-slot rotation state files (`rotation-*.state`). Themes are *preferences* — different concern from the model registry — so they live in their own file.

## Design

### Architecture

Three new Go units and two consumers:

| Unit | Responsibility |
|---|---|
| `internal/themes/themes.go` | Built-in theme registry; exported `Theme` struct; `Builtins()`, `Get()`, `Token()`, `AvailableList()` |
| `internal/themes/load.go` | Load/save/remove `themes.toml` from `~/.config/agent-wt/` |
| `cmd/wt/commands_config.go` | `wt config` cobra command with `theme` subcommand (list/show/set/unset) and `path` helper |
| `cmd/wt/helpers.go` (consumer) | `renderTable` takes the active theme; replaces hardcoded `borderStyle` |
| `internal/tui/` (consumer) | `errorStyle`, picker styles, view rendering take the active theme |

The `app` struct in `cmd/wt/app.go` grows a `theme themes.Theme` field, loaded once in `newApp()` alongside `config.Load()`. `tui.Run()` receives the theme as a parameter and stashes it in `model.theme`. The TUI's `View()` calls `theme.Token(name)` for every styled surface.

### Data shapes

```go
// internal/themes/themes.go
package themes

import "github.com/charmbracelet/lipgloss"

type Theme struct {
    Name   string
    Tokens map[string]lipgloss.AdaptiveColor
}
```

`AdaptiveColor` is lipgloss's built-in type with `Light` and `Dark` string fields. lipgloss picks which to render based on terminal background detection. Honors `NO_COLOR` automatically.

### Token set (9 tokens)

| Token | Used by |
|---|---|
| `border` | Table borders in `wt models`, `wt agents` |
| `error` | Error text in TUI (new-worktree prompt, list errors, status messages) |
| `header` | `phaseModelView`'s "agent: … \| tag: …" header |
| `dim` | Secondary/muted text (picker footer hints) |
| `accent` | Highlights — active cursor, "current" branch marker |
| `selected` | Selected picker row (overrides `bubbles/list` default) |
| `unselected` | Unselected picker row (overrides `bubbles/list` default) |
| `warning` | Default-branch warning, ollama availability warning |
| `success` | Reserved for future positive confirmations |

### Built-in themes

Four themes ship with the binary. Exact hex values are tuned during implementation; directions are:

1. **`default`** — the existing look, ported. ANSI 240 border, ANSI 9 error, ANSI 12 (blue) accent, ANSI 245 dim. Subtle, "this is just a CLI."

2. **`solarized`** — warm palette inspired by the classic Solarized theme. Border `#93a1a1`, error `#dc322f`, header `#b58900` (yellow), accent `#268bd2` (blue), dim `#586e75`.

3. **`mono`** — grayscale + a single blue accent. All tokens grayscale (ANSI 240 → 250 range); accent is the only color (ANSI 12). For minimal visual noise.

4. **`tokyo-night`** — Enkia's [Tokyo Night](https://tokyonight.org/). Night/Day pair designed by the same author to be the same theme viewed differently — pairs naturally with `AdaptiveColor`. Border `#3b4261` (Day: `#a8a9b4`), error `#f7768e` (Day: `#d15f81`), header `#bb9af7` (Day: `#8c70c7`), accent `#7aa2f7` (Day: `#3760bf`), dim `#565f89` (Day: `#96949e`). Hex values are draft, refine during implementation against the official palette.

Theme names are case-insensitive in lookups but stored canonically (lowercase, hyphenated).

### `Token` fallback chain

```go
func (t Theme) Token(name string) lipgloss.AdaptiveColor {
    if c, ok := t.Tokens[name]; ok {
        return c
    }
    return Default.Tokens[name]
}
```

A theme doesn't have to redefine every token to be valid — it inherits anything it omits from `Default`. Tests verify both that every theme has all 9 tokens *and* that `Default` itself has all 9 tokens (so the fallback chain is safe).

### On-disk format (`themes.toml`)

```toml
theme = "tokyo-night"
```

One key, one value, one file. Comments allowed (TOML permits). Unknown keys ignored (forward compat).

### Loader contract

```go
// internal/themes/load.go
package themes

// Load reads the active theme name from ~/.config/agent-wt/themes.toml.
// Returns (Default, false, nil) if the file is missing.
// Returns (Default, false, err) if the file exists but is malformed or has an unknown theme name.
func Load() (Theme, bool, error)
```

The `(Theme, bool, error)` shape distinguishes "user has never set a theme" (the normal startup state) from "user picked a theme name that doesn't exist" (an error we surface).

| State of `themes.toml` | Loader returns |
|---|---|
| File missing | `(Default, false, nil)` |
| File empty | `(Default, false, nil)` |
| `theme = "<valid>"` | `(theme, true, nil)` |
| `theme = "<unknown>"` | `(Default, false, err)` |
| `theme = ""` | `(Default, false, err)` |
| Multiple `theme` keys | `(Default, false, err)` |
| Malformed TOML | `(Default, false, err)` |
| Unknown keys | `(theme, true, nil)` — ignored |
| Permission denied | `(Default, false, err)` |

### Test seam

`themes.go` exposes a package-level `dirFunc = config.Dir` so tests can override the config directory:

```go
// internal/themes/themes.go
var dirFunc = config.Dir

// in tests:
dirFunc = func() string { return t.TempDir() }
defer func() { dirFunc = config.Dir }()
```

This avoids `os.Setenv` (which doesn't compose well across parallel tests) and keeps production code simple. Matches the convention used elsewhere in the codebase (`worktree.RepoRoot()` follows the same pattern).

### Command surface

```
wt config <subcommand> [flags]
```

Registered in `cmd/wt/main.go` alongside `modelsCmd`, `agentsCmd`, `rotateCmd`. No persistent flags.

#### `wt config` (no subcommand)

Prints a short help message listing available subcommands.

#### `wt config path`

```
$ wt config path
~/.config/agent-wt
```

Prints the config directory. Discovery helper for users who want to inspect files by hand.

#### `wt config theme list`

```
$ wt config theme list
default       subtle, terminal-native colors
solarized     warm palette inspired by Solarized
mono          grayscale with a single blue accent
tokyo-night   Enkia's Tokyo Night (Night/Day pair)
```

A 2-column table (name + short description), sorted alphabetically. Reuses `renderTable` from `cmd/wt/helpers.go` (which now takes a theme). Each theme name is rendered in *that theme's accent color* — users can scan the list and immediately see the four themes look different.

#### `wt config theme show [<name>]`

```
$ wt config theme show solarized
solarized — warm palette inspired by Solarized

  border       #93a1a1 / #93a1a1  dark light
  error        #dc322f / #dc322f  dark light
  header       #b58900 / #b58900  dark light
  dim          #586e75 / #586e75  dark light
  accent       #268bd2 / #268bd2  dark light
  selected     ...                ...
  unselected   ...                ...
  warning      ...                ...
  success      ...                ...

  set with: wt config theme set solarized
```

Each token row is a single line. Format: `<token name>  <dark hex> / <light hex>  <dark preview> <light preview>`. The "dark" and "light" preview words render in the token's own dark/light color respectively — the user sees both the hex *and* a live color sample, side-to-side. The "dark"/"light" labels are colored text, not hex codes.

If `<name>` is omitted, shows the *active* theme. If the name doesn't match a built-in, errors with the list of available names (so users can self-correct).

#### `wt config theme set <name>`

```
$ wt config theme set tokyo-night
wt: theme set to "tokyo-night" (effective on next wt launch)
```

Writes the active theme name to `themes.toml` via `WriteFileAtomic` (already exists in `internal/config`). Status message to stderr notes it takes effect on the *next* launch — themes don't hot-reload. Unknown names error without writing (atomic: never leave a broken file).

#### `wt config theme` (no action)

```
$ wt config theme
active: tokyo-night
available: default, solarized, mono, tokyo-night
```

Convenience for "what do I have set right now." The active theme name is rendered in its accent color. Available names listed as a comma-separated hint.

#### `wt config theme unset`

```
$ wt config theme unset
wt: no theme set (using "default")
```

Removes `themes.toml` (or rewrites it without the theme key — implementation picks the simpler path: deletion). Equivalent to "go back to defaults."

### Error handling

#### Loader errors

| Condition | Message |
|---|---|
| Unknown theme name in file | `wt: unknown theme "foo" in themes.toml — available: default, solarized, mono, tokyo-night` |
| Empty theme value | `wt: theme name in themes.toml is empty — available: default, solarized, mono, tokyo-night` |
| Duplicate `theme` keys | `wt: themes.toml has duplicate "theme" key` |
| Malformed TOML | `wt: themes.toml is malformed: <toml error>` |
| Permission denied | `wt: failed to read themes.toml: <os error>` |

#### Writer errors

| Condition | Message |
|---|---|
| Unknown theme name | `wt: unknown theme "foo" — available: default, solarized, mono, tokyo-night` |
| Empty theme name | `wt: theme name cannot be empty — available: default, solarized, mono, tokyo-night` |
| Write failure | `wt: failed to write themes.toml: <os error>` |
| `unset` and file doesn't exist | `wt: no theme set (already using "default")` (no error, exit 0) |

#### Atomic write semantics

`WriteFileAtomic` writes to `themes.toml.tmp` first, then `os.Rename`s over the original. A failure between write and rename leaves the original intact. We never produce a half-written `themes.toml`.

#### Error message conventions

- All messages prefixed `wt: ` (matches existing project style: `wt: config error:`, `wt: failed to auto-install main guard:`).
- Stdout for data, stderr for status. Errors always go to stderr.
- Exit code: non-zero for any error, zero for success.
- No emoji, no ANSI color in error messages (must be readable on any terminal).

The "wt: theme set to …" confirmation on success goes to stderr so `wt config theme set tokyo-night > /tmp/foo` writes nothing to stdout (stdout is reserved for parseable data).

### Data flow

#### Flow 1: Normal `wt` launch

```
$ wt
```

1. `main()` → `rootCmd()` → `newApp()`.
2. `newApp()` calls `config.Load()` and `themes.Load()`.
3. `app` struct holds `{cfg, theme}`.
4. RunE path:
   - Interactive TUI: `tui.Run(yolo, agent, tags, family, extraArgs)` → `model{cfg, theme, …}`. `View()` calls `theme.Token(name)` for every styled surface.
   - Flag paths (`-W`, `--cwd`): `wt models` and `wt agents` are themed via `renderTable(theme)`; `wt rotate` doesn't print themed output.
   - Subcommand paths: same as flag paths.

Theme application is read-only at startup. No goroutine watches `themes.toml` for changes.

#### Flow 2: `wt config theme set`

```
$ wt config theme set tokyo-night
```

1. `main()` → `rootCmd()` → `newApp()`. Theme is loaded but **not applied to this command's output** (user is actively changing the theme; prompt stays neutral).
2. Cobra routes to `themeSetCmd.RunE`.
3. Validates name via `themes.Get("tokyo-night")`. Errors without writing if unknown.
4. Writes `themes.toml` via `WriteFileAtomic`.
5. Prints status to stderr.
6. Exits 0.

#### Flow 3: `wt config theme list` / `show` / unset

`list`: walks `themes.Builtins()`, sorts by name, renders themed table with names in their accent colors.

`show`: looks up named theme (or active if no name), renders token grid with dark/light previews.

`unset`: `os.Remove(themes.toml)`. No-op if file doesn't exist.

### Implementation cut list (YAGNI)

- No user-customizable palette.
- No theme hot-reload.
- No per-command theme overrides.
- No "current theme" indicator in TUI footer.
- No TOML parsing of theme definitions (themes are Go structs).
- No theme aliases (`ls` for `list`, etc.).
- No `wt config theme export`.
- No `wt config theme preview` (would need TUI demo or fancy ANSI block — YAGNI).

## Testing

Following the project's convention (every `Test*` function has a top-level `//` block explaining what it tests and why). Tests use `t.TempDir()` for the config directory via the test seam.

### `internal/themes/themes_test.go`

- `TestBuiltins_ReturnsAllFourThemes` — `Builtins()` returns exactly 4 documented names.
- `TestBuiltins_HasAllNineTokens` — every theme has all 9 token keys.
- `TestDefault_HasAllNineTokens` — `Default` itself has all 9 tokens.
- `TestGet_ExactMatch` — `Get("solarized")` returns Solarized.
- `TestGet_CaseInsensitive` — `Get("SOLARIZED")` returns Solarized.
- `TestGet_Unknown` — `Get("nope")` returns `(_, false)`.
- `TestGet_Empty` — `Get("")` returns `(_, false)`.
- `TestToken_UnknownFallsBackToDefault` — `theme.Token("nonexistent")` returns `Default.Tokens["nonexistent"]`.
- `TestToken_KnownReturnsThemesValue` — `theme.Token("accent")` returns the theme's own accent.
- `TestAvailableList_StableOrder` — `AvailableList()` returns the same order on every call.

### `internal/themes/load_test.go`

- `TestLoad_MissingFile` — file missing → `(Default, false, nil)`.
- `TestLoad_EmptyFile` — empty file → `(Default, false, nil)`.
- `TestLoad_ValidTheme` — `theme = "solarized"` → `(solarized, true, nil)`.
- `TestLoad_UnknownTheme` — `theme = "foo"` → error mentioning available names.
- `TestLoad_EmptyThemeValue` — `theme = ""` → error.
- `TestLoad_DuplicateThemeKey` — two `theme` keys → error.
- `TestLoad_MalformedTOML` — garbage input → error.
- `TestLoad_UnknownKeysIgnored` — extra unknown keys → still returns the named theme.
- `TestLoad_CaseInsensitiveThemeName` — `theme = "SOLARIZED"` → Solarized.
- `TestLoad_PermissionDenied` — chmod 0000 → error. Skip on platforms where chmod doesn't take effect.
- `TestSave_AtomicWrite` — write succeeds; pre-existing file content is preserved on failure.
- `TestSave_PreservesFileOnFailure` — unwritable directory → original file untouched.
- `TestUnset_RemovesFile` — `themes.toml` exists, `Unset` removes it.
- `TestUnset_MissingFile` — `themes.toml` missing → no-op success.

### `cmd/wt/commands_config_test.go`

- `TestConfigCmd_NoSubcommand_PrintsHelp` — `wt config` exits 0, prints help.
- `TestConfigCmd_UnknownSubcommand` — `wt config foo` errors, exits non-zero.
- `TestConfigThemeCmd_NoAction_PrintsHelp` — `wt config theme` exits 0, prints help.
- `TestConfigThemeShow_NoArg_ShowsActive` — `wt config theme show` shows active theme.
- `TestConfigThemeShow_UnknownName_Errors` — error message contains available names.
- `TestConfigThemeSet_UnknownName_DoesNotWrite` — error AND file unchanged.
- `TestConfigThemeSet_ValidName_WritesFile` — file contains `theme = "tokyo-night"`, exit 0.
- `TestConfigThemeSet_EmptyName_Errors` — empty isn't valid.
- `TestConfigThemeList_PrintsAllThemes` — output contains all 4 names.
- `TestConfigTheme_ShowsActive` — `wt config theme` shows active.
- `TestConfigPath_PrintsDir` — `wt config path` prints the dir.

### Smoke tests (`make test` additions)

```bash
./bin/wt config                        # prints help, exits 0
./bin/wt config theme                   # prints active theme, exits 0
./bin/wt config theme list              # prints 4 themes, exits 0
./bin/wt config theme show default      # shows default tokens, exits 0
./bin/wt config theme set tokyo-night   # sets theme, exits 0
./bin/wt config theme unset             # unsets, exits 0
./bin/wt -W nonexistent -A claude --init  # regression check on existing launch path
```

### Coverage targets

| Package | Tests | Focus |
|---|---|---|
| `internal/themes` | 25+ | Registry, fallback chain, all 6 loader branches, atomic write, case-insensitivity |
| `cmd/wt` (config subcommand) | 11+ | Cobra wiring, error messages, atomic write guarantee |

### What we don't test

- Visual rendering (no headless terminal harness; lipgloss handles it).
- Performance (one-time per-launch read; not a concern).
- Cross-terminal compatibility (lipgloss's job).

## Open questions

None at design time. Implementation may surface palette tuning questions for the built-in themes — those happen during implementation, not design.

## Files affected

**New:**
- `internal/themes/themes.go`
- `internal/themes/themes_test.go`
- `internal/themes/load.go`
- `internal/themes/load_test.go`
- `cmd/wt/commands_config.go`
- `cmd/wt/commands_config_test.go`

**Modified:**
- `cmd/wt/main.go` — register `configCmd(a)` alongside existing subcommands.
- `cmd/wt/app.go` — `app` struct gains `theme themes.Theme` field; `newApp()` loads it.
- `cmd/wt/helpers.go` — `renderTable` takes a theme; `borderStyle` becomes a function.
- `cmd/wt/commands.go` — `modelsCmd` and `agentsCmd` pass theme to `renderTable`.
- `internal/tui/app.go` — `model` struct gains `theme themes.Theme` field; `Run` accepts theme parameter.
- `internal/tui/new_worktree.go` — `errorStyle` becomes a function taking the theme.
- `internal/tui/model_list.go` — themed picker rendering.
- `Makefile` — add `wt config` smoke tests to `make test`.
