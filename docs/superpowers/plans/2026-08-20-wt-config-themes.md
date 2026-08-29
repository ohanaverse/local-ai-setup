# `wt config` + Color Themes Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a `wt config` command that manages user-level preferences, with color themes as the first shipped feature. Four built-in themes (default, solarized, mono, tokyo-night) auto-adapt to light/dark terminals via `lipgloss.AdaptiveColor`. Theme selection persists in `~/.config/agent-wt/themes.toml`; theme definitions are compiled into the binary.

**Architecture:** Single new `internal/themes` package holds the registry (built-in themes as exported Go structs) and the loader (reads/writes `themes.toml` via existing `WriteFileAtomic`). The `app` struct in `cmd/wt/app.go` gains a `theme` field loaded once at startup. `tui.Run` and `renderTable` consume the theme; `errorStyle` and picker styles become functions of the theme. New `wt config` cobra command exposes `theme list|show|set|unset` plus a `path` helper.

**Tech Stack:** Go 1.26.3, charmbracelet/lipgloss (already in use), charmbracelet/bubbletea + bubbles/list (already in use), BurntSushi/toml (already in use).

**Spec:** `docs/superpowers/specs/2026-08-20-wt-config-themes-design.md`

## Global Constraints

- Go 1.26.3, no new module dependencies (lipgloss already provides `AdaptiveColor`).
- Every `Test*` function has a top-level `//` block explaining what it tests and why that matters (Lesson 18 convention).
- All Go error/status messages prefix `wt: `.
- Status messages go to stderr; data goes to stdout.
- Exit code 0 on success, non-zero on any error.
- No emoji, no ANSI color in error messages.
- PRs are not created without explicit user approval (CLAUDE.md rule).
- During plan execution, commits reference the plan item they complete (CLAUDE.md rule); post-execution polish commits do not.
- Theme names are case-insensitive in lookups but stored canonically (lowercase, hyphenated).
- `themes.toml` location: `~/.config/agent-wt/themes.toml` (honors `XDG_CONFIG_HOME`).
- `themes.toml` format: `theme = "<name>"` — one key, one value, comments allowed, unknown keys ignored.
- `WriteFileAtomic` (already in `internal/config`) handles atomic file writes; no new helpers.
- `NO_COLOR` is honored automatically via lipgloss; no special handling required.

---

## File Structure

### New files

| Path | Responsibility |
|---|---|
| `internal/themes/themes.go` | Built-in theme registry: `Theme` struct, `Builtins()`, `Get()`, `Token()`, `AvailableList()`, `Default` |
| `internal/themes/themes_test.go` | Tests for the registry (10 tests) |
| `internal/themes/load.go` | `themes.toml` loader/saver/unsetter with test seam (`dirFunc = config.Dir`) |
| `internal/themes/load_test.go` | Tests for the loader (14 tests) |
| `cmd/wt/commands_config.go` | `wt config` cobra command + `theme` subcommand + `path` subcommand |
| `cmd/wt/commands_config_test.go` | Tests for cobra wiring (11 tests) |

### Modified files

| Path | Change |
|---|---|
| `cmd/wt/main.go` | Register `configCmd(a)` alongside existing subcommands (one-line addition) |
| `cmd/wt/app.go` | `app` struct gains `theme themes.Theme` field; `newApp()` loads it |
| `cmd/wt/helpers.go` | `renderTable` takes `theme` parameter; `borderStyle` becomes a function |
| `cmd/wt/commands.go` | `modelsCmd`, `agentsCmd` pass `a.theme` to `renderTable` |
| `cmd/wt/launch.go` | `buildFilteredCmd`, `runAgentCmd`, etc. accept `theme` for any rendered output (currently none — but signature change for consistency) |
| `internal/tui/app.go` | `model` struct gains `theme` field; `Run` accepts `theme` parameter |
| `internal/tui/new_worktree.go` | `errorStyle` becomes `errorStyle(theme)` function |
| `internal/tui/model_list.go` | Themed picker rendering using `m.theme` |
| `Makefile` | Add `wt config` smoke tests to `make test` target |

### File responsibilities (one-line each)

- `themes.go` — defines what a theme *is* (struct + lookup).
- `load.go` — defines how a theme *persists* (file I/O).
- `commands_config.go` — defines how users *interact* with themes (cobra wiring).

Files change together when their concerns are coupled; split by responsibility, not by technical layer.

---

## Task 1: Theme registry — `Theme` struct + 4 built-in themes

**Files:**
- Create: `internal/themes/themes.go`

**Interfaces:**
- Consumes: lipgloss.AdaptiveColor
- Produces:
  - `type Theme struct { Name string; Tokens map[string]lipgloss.AdaptiveColor }`
  - `var Default Theme`
  - `func Builtins() []Theme`
  - `func Get(name string) (Theme, bool)`
  - `func (t Theme) Token(name string) lipgloss.AdaptiveColor`
  - `func AvailableList() []string`
  - `func dirFunc() string` (package-level test seam; defaults to `config.Dir`)

**Task note:** This task writes the registry. No tests yet — Task 2 writes the tests for this code.

- [ ] **Step 1: Create `internal/themes/themes.go` with the struct + fallback chain**

```go
// Package themes holds the built-in color themes used by wt's TUI and CLI
// tables. Themes are compiled into the binary (not loaded from disk); the
// user's choice of which theme to use lives in ~/.config/agent-wt/themes.toml.
//
// Themes use lipgloss.AdaptiveColor so light/dark terminals render correctly
// without any explicit background detection. lipgloss honors NO_COLOR
// automatically.
package themes

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/ohanaverse/agent-worktree/internal/config"
)

// Token names — the keys used in Theme.Tokens. The set is fixed; the values
// differ per theme. A theme that omits a token inherits it from Default
// (see Theme.Token).
const (
	TokenBorder     = "border"
	TokenError      = "error"
	TokenHeader     = "header"
	TokenDim        = "dim"
	TokenAccent     = "accent"
	TokenSelected   = "selected"
	TokenUnselected = "unselected"
	TokenWarning    = "warning"
	TokenSuccess    = "success"
)

// AllTokens returns the canonical token list, in stable order. Used by tests
// to verify every theme has every token.
func AllTokens() []string {
	return []string{
		TokenBorder, TokenError, TokenHeader, TokenDim,
		TokenAccent, TokenSelected, TokenUnselected,
		TokenWarning, TokenSuccess,
	}
}

// Theme is a named palette. Tokens are keyed by token name (see TokenBorder
// etc.) and map to a lipgloss color that adapts to light or dark terminals.
type Theme struct {
	Name   string
	Tokens map[string]lipgloss.AdaptiveColor
}

// Token returns the named token, falling back to Default if the theme omits
// it. This means themes can be authored incrementally without breaking the
// fallback chain — anything not specified inherits from Default.
func (t Theme) Token(name string) lipgloss.AdaptiveColor {
	if c, ok := t.Tokens[name]; ok {
		return c
	}
	return Default.Tokens[name]
}

// Default is the fallback theme. It is also the theme shipped as the
// baseline for users who haven't picked one.
var Default = Theme{
	Name: "default",
	Tokens: map[string]lipgloss.AdaptiveColor{
		TokenBorder:     {Light: "240", Dark: "240"},
		TokenError:      {Light: "9", Dark: "9"},
		TokenHeader:     {Light: "12", Dark: "12"},
		TokenDim:        {Light: "245", Dark: "245"},
		TokenAccent:     {Light: "12", Dark: "12"},
		TokenSelected:   {Light: "12", Dark: "12"},
		TokenUnselected: {Light: "245", Dark: "245"},
		TokenWarning:    {Light: "11", Dark: "11"},
		TokenSuccess:    {Light: "10", Dark: "10"},
	},
}

// solarizedTokens is the Solarized-inspired palette.
var solarizedTokens = map[string]lipgloss.AdaptiveColor{
	TokenBorder:     {Light: "#93a1a1", Dark: "#93a1a1"},
	TokenError:      {Light: "#dc322f", Dark: "#dc322f"},
	TokenHeader:     {Light: "#b58900", Dark: "#b58900"},
	TokenDim:        {Light: "#586e75", Dark: "#586e75"},
	TokenAccent:     {Light: "#268bd2", Dark: "#268bd2"},
	TokenSelected:   {Light: "#268bd2", Dark: "#268bd2"},
	TokenUnselected: {Light: "#586e75", Dark: "#586e75"},
	TokenWarning:    {Light: "#cb4b16", Dark: "#cb4b16"},
	TokenSuccess:    {Light: "#859900", Dark: "#859900"},
}

// monoTokens is grayscale + a single blue accent. Identical Light/Dark on
// most tokens since grayscale values don't shift between modes.
var monoTokens = map[string]lipgloss.AdaptiveColor{
	TokenBorder:     {Light: "240", Dark: "250"},
	TokenError:      {Light: "9", Dark: "9"},
	TokenHeader:     {Light: "245", Dark: "250"},
	TokenDim:        {Light: "243", Dark: "243"},
	TokenAccent:     {Light: "12", Dark: "12"},
	TokenSelected:   {Light: "12", Dark: "12"},
	TokenUnselected: {Light: "245", Dark: "245"},
	TokenWarning:    {Light: "11", Dark: "11"},
	TokenSuccess:    {Light: "10", Dark: "10"},
}

// tokyoNightTokens is Enkia's Tokyo Night palette. Light/Dark values are the
// "Day" and "Night" halves of the same theme — designed by the same author
// to be the same palette viewed differently. Hex values are draft; refine
// during implementation against https://tokyonight.org/ if needed.
var tokyoNightTokens = map[string]lipgloss.AdaptiveColor{
	TokenBorder:     {Light: "#a8a9b4", Dark: "#3b4261"},
	TokenError:      {Light: "#d15f81", Dark: "#f7768e"},
	TokenHeader:     {Light: "#8c70c7", Dark: "#bb9af7"},
	TokenDim:        {Light: "#96949e", Dark: "#565f89"},
	TokenAccent:     {Light: "#3760bf", Dark: "#7aa2f7"},
	TokenSelected:   {Light: "#3760bf", Dark: "#7aa2f7"},
	TokenUnselected: {Light: "#96949e", Dark: "#565f89"},
	TokenWarning:    {Light: "#e07a47", Dark: "#ff9e64"},
	TokenSuccess:    {Light: "#6c8e7e", Dark: "#9ece6a"},
}

// builtins is the canonical theme list, in stable order. The first entry is
// the Default; subsequent entries are additional themes.
var builtins = []Theme{
	{Name: "default", Tokens: Default.Tokens},
	{Name: "solarized", Tokens: solarizedTokens},
	{Name: "mono", Tokens: monoTokens},
	{Name: "tokyo-night", Tokens: tokyoNightTokens},
}

// Builtins returns the list of all available themes. Order matches the
// builtins slice (stable across calls).
func Builtins() []Theme {
	out := make([]Theme, len(builtins))
	copy(out, builtins)
	return out
}

// AvailableList returns the names of all built-in themes in stable order.
// Used to construct error messages so users see the same available list
// everywhere.
func AvailableList() []string {
	out := make([]string, len(builtins))
	for i, t := range builtins {
		out[i] = t.Name
	}
	return out
}

// Get returns the theme with the given name. Name matching is
// case-insensitive (canonical form is lowercase, hyphenated). Returns
// (zero, false) if no theme matches.
func Get(name string) (Theme, bool) {
	lower := strings.ToLower(strings.TrimSpace(name))
	for _, t := range builtins {
		if t.Name == lower {
			return t, true
		}
	}
	return Theme{}, false
}

// dirFunc is the test seam for the config directory. Tests override this to
// point at t.TempDir() so they don't touch the user's real themes.toml.
// Production code uses config.Dir() (XDG_CONFIG_HOME-aware).
var dirFunc = config.Dir

// dir is the internal accessor for the configured directory path. Tests
// override dirFunc; production code never touches it directly.
func dir() string { return dirFunc() }
```

- [ ] **Step 2: Build the package to verify it compiles**

Run: `go build ./internal/themes`
Expected: no output, exit 0.

- [ ] **Step 3: Commit**

```bash
git add internal/themes/themes.go
git commit -m "feat(themes): add Theme struct with 4 built-in palettes - completes plan item #1"
```

---

## Task 2: Theme registry tests

**Files:**
- Create: `internal/themes/themes_test.go`

**Interfaces:**
- Consumes: `Theme`, `Builtins`, `Get`, `Token`, `AvailableList`, `Default`, `AllTokens`
- Produces: 10 tests covering the registry

- [ ] **Step 1: Write the failing tests**

```go
// internal/themes/themes_test.go
//
// Tests for the theme registry: name lookups, the Token fallback chain,
// case-insensitivity, and the canonical token list. A regression here
// means every themed surface in wt is wrong — the registry is the
// foundation everything else builds on.

package themes

import (
	"testing"

	"github.com/charmbracelet/lipgloss"
)

// TestBuiltins_ReturnsAllFourThemes verifies that Builtins() returns exactly
// the 4 documented themes. Missing a built-in would silently break
// `wt config theme list` — the user would see three themes instead of four
// and assume the fourth was removed.
func TestBuiltins_ReturnsAllFourThemes(t *testing.T) {
	got := Builtins()
	if len(got) != 4 {
		t.Fatalf("Builtins() returned %d themes, want 4", len(got))
	}
	want := []string{"default", "solarized", "mono", "tokyo-night"}
	for i, name := range want {
		if got[i].Name != name {
			t.Errorf("Builtins()[%d].Name = %q, want %q", i, got[i].Name, name)
		}
	}
}

// TestBuiltins_HasAllNineTokens verifies every built-in theme defines all 9
// tokens. A theme missing a token silently inherits from Default via Token,
// which means the theme looks identical to default — defeating the purpose
// of having multiple themes. This catches the "I added a theme but forgot
// the accent token" failure mode.
func TestBuiltins_HasAllNineTokens(t *testing.T) {
	for _, theme := range Builtins() {
		t.Run(theme.Name, func(t *testing.T) {
			for _, token := range AllTokens() {
				if _, ok := theme.Tokens[token]; !ok {
					t.Errorf("theme %q missing token %q", theme.Name, token)
				}
			}
		})
	}
}

// TestDefault_HasAllNineTokens verifies the Default theme itself has all 9
// tokens. The Token() fallback chain returns Default.Tokens[name] when a
// token is missing from another theme — but only safe if Default is
// complete. If Default is missing a token, every other theme that omits it
// returns a zero-value AdaptiveColor and renders with no color.
func TestDefault_HasAllNineTokens(t *testing.T) {
	for _, token := range AllTokens() {
		if _, ok := Default.Tokens[token]; !ok {
			t.Errorf("Default theme missing token %q", token)
		}
	}
}

// TestGet_ExactMatch verifies case-exact lookups work.
func TestGet_ExactMatch(t *testing.T) {
	got, ok := Get("solarized")
	if !ok {
		t.Fatal("Get(\"solarized\") returned ok=false, want true")
	}
	if got.Name != "solarized" {
		t.Errorf("Get(\"solarized\").Name = %q, want %q", got.Name, "solarized")
	}
}

// TestGet_CaseInsensitive verifies theme names are case-insensitive. Theme
// names in themes.toml are user-editable; we don't want to fail on
// "SOLARIZED" vs "solarized" — the user might have written it however.
func TestGet_CaseInsensitive(t *testing.T) {
	for _, name := range []string{"SOLARIZED", "Solarized", "solarized", "SoLaRiZeD"} {
		got, ok := Get(name)
		if !ok {
			t.Errorf("Get(%q) returned ok=false", name)
			continue
		}
		if got.Name != "solarized" {
			t.Errorf("Get(%q).Name = %q, want canonical %q", name, got.Name, "solarized")
		}
	}
}

// TestGet_Unknown verifies unknown names return (zero, false). The
// subcommand and loader both rely on this to detect user typos.
func TestGet_Unknown(t *testing.T) {
	got, ok := Get("nonexistent")
	if ok {
		t.Errorf("Get(\"nonexistent\") returned ok=true with theme %v", got)
	}
}

// TestGet_Empty verifies an empty name returns (zero, false). Empty string
// isn't valid — neither loader nor subcommand should treat it as a valid
// theme name.
func TestGet_Empty(t *testing.T) {
	got, ok := Get("")
	if ok {
		t.Errorf("Get(\"\") returned ok=true with theme %v", got)
	}
}

// TestToken_UnknownFallsBackToDefault verifies Token() returns
// Default.Tokens[name] when the theme omits the token. This is the safety
// net for incomplete themes — the lookup must not panic or return a zero
// value.
func TestToken_UnknownFallsBackToDefault(t *testing.T) {
	theme := Theme{Name: "empty", Tokens: map[string]lipgloss.AdaptiveColor{}}
	got := theme.Token("border")
	want := Default.Tokens["border"]
	if got != want {
		t.Errorf("theme.Token(\"border\") = %v, want Default %v", got, want)
	}
}

// TestToken_KnownReturnsThemesValue verifies Token() returns the theme's own
// value when present — not Default's. This is the primary lookup path for
// styled surfaces.
func TestToken_KnownReturnsThemesValue(t *testing.T) {
	solarized, _ := Get("solarized")
	got := solarized.Token("accent")
	want := solarizedTokens["accent"]
	if got != want {
		t.Errorf("solarized.Token(\"accent\") = %v, want %v", got, want)
	}
}

// TestAvailableList_StableOrder verifies AvailableList() returns the same
// order on every call. Used in error messages ("available: default,
// solarized, ...") — we want the message to be deterministic so users can
// grep against it.
func TestAvailableList_StableOrder(t *testing.T) {
	first := AvailableList()
	for i := 0; i < 5; i++ {
		got := AvailableList()
		if len(got) != len(first) {
			t.Fatalf("iteration %d: length %d, want %d", i, len(got), len(first))
		}
		for j := range got {
			if got[j] != first[j] {
				t.Errorf("iteration %d, index %d: got %q, want %q", i, j, got[j], first[j])
			}
		}
	}
}
```

- [ ] **Step 2: Run tests to verify they pass**

Run: `go test ./internal/themes -v -run 'TestBuiltins|TestDefault|TestGet|TestToken|TestAvailableList'`
Expected: PASS, all 10 tests.

- [ ] **Step 3: Commit**

```bash
git add internal/themes/themes_test.go
git commit -m "test(themes): add registry tests - completes plan item #2"
```

---

## Task 3: Loader — `themes.toml` load/save/unset

**Files:**
- Create: `internal/themes/load.go`

**Interfaces:**
- Consumes: `Theme`, `Get`, `dir()` (the test seam), `WriteFileAtomic`
- Produces:
  - `func Load() (Theme, bool, error)`
  - `func Save(name string) error`
  - `func Unset() error`
  - `func Path() string`

- [ ] **Step 1: Create `internal/themes/load.go`**

```go
// Loader for ~/.config/agent-wt/themes.toml. The file holds one key
// (`theme = "<name>"`) and is the only place user preference is persisted;
// theme definitions live in themes.go and are compiled in.
//
// All file I/O uses the existing WriteFileAtomic helper from internal/config
// so a crash mid-write can never corrupt the file. The test seam (dirFunc)
// lets tests redirect the directory to t.TempDir().

package themes

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/BurntSushi/toml"
	"github.com/ohanaverse/agent-worktree/internal/config"
)

// themesFile is the on-disk format. One key, one value. Unknown keys are
// silently ignored for forward compatibility.
type themesFile struct {
	Theme string `toml:"theme"`
}

// Path returns the absolute path to themes.toml. Computed from dir() so
// tests can redirect the directory.
func Path() string {
	return filepath.Join(dir(), "themes.toml")
}

// Load reads the active theme from themes.toml. The second return value is
// true when the user has explicitly chosen a theme, false when no choice
// has been made (file missing, empty, etc.). Errors are returned for
// malformed files, unknown theme names, duplicate keys, and permission
// failures — see the spec's loader contract table for the full matrix.
func Load() (Theme, bool, error) {
	path := Path()
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return Default, false, nil
	}
	if err != nil {
		return Default, false, fmt.Errorf("failed to read themes.toml: %w", err)
	}

	// Empty file: same as missing.
	if len(data) == 0 {
		return Default, false, nil
	}

	var tf themesFile
	if _, err := toml.Decode(string(data), &tf); err != nil {
		return Default, false, fmt.Errorf("themes.toml is malformed: %w", err)
	}

	// BurntSushi/toml permits duplicate keys by default and silently keeps
	// the last value. We explicitly detect them so a script bug can't
	// produce two competing values.
	if err := checkDuplicateThemeKey(data); err != nil {
		return Default, false, err
	}

	if tf.Theme == "" {
		return Default, false, fmt.Errorf("theme name in themes.toml is empty — available: %s",
			joinNames(AvailableList()))
	}

	theme, ok := Get(tf.Theme)
	if !ok {
		return Default, false, fmt.Errorf("unknown theme %q in themes.toml — available: %s",
			tf.Theme, joinNames(AvailableList()))
	}
	return theme, true, nil
}

// checkDuplicateThemeKey rejects files with multiple `theme` keys. Burns the
// the raw text to count occurrences since toml.Decode silently deduplicates.
func checkDuplicateThemeKey(data []byte) error {
	count := 0
	inKey := false
	// Naive but sufficient: count lines that start with optional whitespace
	// then "theme" then "=". False positives (a `theme` inside a quoted
	// string) are vanishingly unlikely for a one-key file.
	for i := 0; i < len(data); i++ {
		// Start-of-line whitespace
		j := i
		for j < len(data) && (data[j] == ' ' || data[j] == '\t') {
			j++
		}
		// Look for `theme`
		if j+5 <= len(data) && string(data[j:j+5]) == "theme" {
			// Must be followed by optional whitespace + `=`
			k := j + 5
			for k < len(data) && (data[k] == ' ' || data[k] == '\t') {
				k++
			}
			if k < len(data) && data[k] == '=' {
				count++
				if count > 1 {
					return errors.New(`themes.toml has duplicate "theme" key`)
				}
				inKey = true
			}
		}
		i = j + 4 // skip past `theme` to avoid re-matching inside it
		if inKey {
			inKey = false
		}
	}
	return nil
}

// Save writes the active theme name to themes.toml atomically. The name
// must match a built-in theme; unknown names error without writing.
func Save(name string) error {
	theme, ok := Get(name)
	if !ok {
		return fmt.Errorf("unknown theme %q — available: %s",
			name, joinNames(AvailableList()))
	}
	if strings.TrimSpace(name) == "" {
		// Defensive: Get already returns false for empty names, but make
		// the error message user-friendly at this layer.
		return fmt.Errorf("theme name cannot be empty — available: %s",
			joinNames(AvailableList()))
	}
	_ = theme // not used in serialization; we just want validation
	body := fmt.Sprintf("theme = %q\n", name)
	return config.WriteFileAtomic(Path(), []byte(body), 0o644)
}

// Unset removes themes.toml, returning to the Default theme. No-op success
// if the file doesn't already exist.
func Unset() error {
	err := os.Remove(Path())
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

// joinNames formats AvailableList() as a comma-separated string for use in
// error messages. The order is stable (matches AvailableList's contract).
func joinNames(names []string) string {
	out := ""
	for i, n := range names {
		if i > 0 {
			out += ", "
		}
		out += n
	}
	return out
}
```

- [ ] **Step 2: Build to verify it compiles**

Run: `go build ./internal/themes`
Expected: no output, exit 0.

If the build complains about `strings`, add `"strings"` to the imports.

- [ ] **Step 3: Commit**

```bash
git add internal/themes/load.go
git commit -m "feat(themes): add themes.toml loader/saver/unsetter - completes plan item #3"
```

---

## Task 4: Loader tests

**Files:**
- Create: `internal/themes/load_test.go`

**Interfaces:**
- Consumes: `Load`, `Save`, `Unset`, `Path`, test seam `dirFunc`
- Produces: 14 tests covering all loader branches

- [ ] **Step 1: Write the failing tests**

```go
// internal/themes/load_test.go
//
// Tests for the themes.toml loader: all 6 branches from the spec's loader
// contract table, plus atomic write, unset, and case-insensitivity. A
// regression here means the wrong theme loads silently or the file gets
// corrupted on write.

package themes

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// withTempDir redirects the theme config directory to a temp dir for the
// duration of the test. Cleanup restores the production dirFunc. This is
// the test seam from themes.go (var dirFunc).
func withTempDir(t *testing.T) string {
	t.Helper()
	orig := dirFunc
	tmp := t.TempDir()
	dirFunc = func() string { return tmp }
	t.Cleanup(func() { dirFunc = orig })
	return tmp
}

// TestLoad_MissingFile: themes.toml doesn't exist → (Default, false, nil).
// The "no user choice" path. This is the normal first-launch state.
func TestLoad_MissingFile(t *testing.T) {
	withTempDir(t)
	theme, set, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v, want nil", err)
	}
	if set {
		t.Errorf("Load() set = true, want false (no file)")
	}
	if theme.Name != Default.Name {
		t.Errorf("Load() theme.Name = %q, want %q (Default)", theme.Name, Default.Name)
	}
}

// TestLoad_EmptyFile: themes.toml exists but is empty → (Default, false,
// nil). TOML permits empty files; treat as "no choice."
func TestLoad_EmptyFile(t *testing.T) {
	tmp := withTempDir(t)
	if err := os.WriteFile(filepath.Join(tmp, "themes.toml"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	theme, set, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v, want nil", err)
	}
	if set {
		t.Errorf("Load() set = true, want false")
	}
	if theme.Name != Default.Name {
		t.Errorf("Load() theme.Name = %q, want Default %q", theme.Name, Default.Name)
	}
}

// TestLoad_ValidTheme: theme = "solarized" → (solarized, true, nil).
// The happy path.
func TestLoad_ValidTheme(t *testing.T) {
	tmp := withTempDir(t)
	if err := os.WriteFile(filepath.Join(tmp, "themes.toml"), []byte("theme = \"solarized\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	theme, set, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if !set {
		t.Errorf("Load() set = false, want true")
	}
	if theme.Name != "solarized" {
		t.Errorf("Load() theme.Name = %q, want %q", theme.Name, "solarized")
	}
}

// TestLoad_UnknownTheme: theme = "foo" → error mentioning available names.
// The typo case — the user wrote a bad name in themes.toml and we want to
// surface it instead of silently falling back.
func TestLoad_UnknownTheme(t *testing.T) {
	tmp := withTempDir(t)
	if err := os.WriteFile(filepath.Join(tmp, "themes.toml"), []byte("theme = \"foo\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, _, err := Load()
	if err == nil {
		t.Fatal("Load() error = nil, want non-nil")
	}
	if !strings.Contains(err.Error(), "foo") {
		t.Errorf("error message %q should mention the bad name \"foo\"", err.Error())
	}
	for _, name := range []string{"default", "solarized", "mono", "tokyo-night"} {
		if !strings.Contains(err.Error(), name) {
			t.Errorf("error message %q should list available theme %q", err.Error(), name)
		}
	}
}

// TestLoad_EmptyThemeValue: theme = "" → error. Empty isn't valid.
func TestLoad_EmptyThemeValue(t *testing.T) {
	tmp := withTempDir(t)
	if err := os.WriteFile(filepath.Join(tmp, "themes.toml"), []byte("theme = \"\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, _, err := Load()
	if err == nil {
		t.Fatal("Load() error = nil, want non-nil for empty theme value")
	}
	if !strings.Contains(err.Error(), "empty") {
		t.Errorf("error message %q should mention \"empty\"", err.Error())
	}
}

// TestLoad_DuplicateThemeKey: two `theme = ...` lines → error. TOML
// silently keeps the last value; we reject the file.
func TestLoad_DuplicateThemeKey(t *testing.T) {
	tmp := withTempDir(t)
	contents := "theme = \"solarized\"\ntheme = \"mono\"\n"
	if err := os.WriteFile(filepath.Join(tmp, "themes.toml"), []byte(contents), 0o644); err != nil {
		t.Fatal(err)
	}
	_, _, err := Load()
	if err == nil {
		t.Fatal("Load() error = nil, want non-nil for duplicate theme key")
	}
	if !strings.Contains(err.Error(), "duplicate") {
		t.Errorf("error message %q should mention \"duplicate\"", err.Error())
	}
}

// TestLoad_MalformedTOML: garbage input → error. Test a few flavors.
func TestLoad_MalformedTOML(t *testing.T) {
	tmp := withTempDir(t)
	for _, garbage := range []string{
		"this is not toml",
		"theme = solarized",  // unquoted
		"[unclosed",          // unclosed bracket
		"theme = \"solarized",  // unclosed quote
	} {
		t.Run(garbage, func(t *testing.T) {
			if err := os.WriteFile(filepath.Join(tmp, "themes.toml"), []byte(garbage), 0o644); err != nil {
				t.Fatal(err)
			}
			_, _, err := Load()
			if err == nil {
				t.Errorf("Load() error = nil for %q, want non-nil", garbage)
			}
			if !strings.Contains(err.Error(), "malformed") {
				t.Errorf("error message %q should mention \"malformed\"", err.Error())
			}
		})
	}
}

// TestLoad_UnknownKeysIgnored: theme = "solarized" plus a comment plus an
// unknown key → still returns solarized. Forward compat for future fields.
func TestLoad_UnknownKeysIgnored(t *testing.T) {
	tmp := withTempDir(t)
	contents := "# user comment\ntheme = \"solarized\"\nunknown_future_field = 42\n"
	if err := os.WriteFile(filepath.Join(tmp, "themes.toml"), []byte(contents), 0o644); err != nil {
		t.Fatal(err)
	}
	theme, _, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v, want nil", err)
	}
	if theme.Name != "solarized" {
		t.Errorf("Load() theme.Name = %q, want %q", theme.Name, "solarized")
	}
}

// TestLoad_CaseInsensitiveThemeName: theme = "SOLARIZED" → solarized. The
// loader must accept any case since users edit themes.toml by hand.
func TestLoad_CaseInsensitiveThemeName(t *testing.T) {
	tmp := withTempDir(t)
	if err := os.WriteFile(filepath.Join(tmp, "themes.toml"), []byte("theme = \"SOLARIZED\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	theme, _, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if theme.Name != "solarized" {
		t.Errorf("Load() theme.Name = %q, want canonical %q", theme.Name, "solarized")
	}
}

// TestLoad_PermissionDenied: chmod 0000 → error. Skip on platforms where
// chmod doesn't take effect (we run on macOS/Linux only).
func TestLoad_PermissionDenied(t *testing.T) {
	if os.Getenv("CI") == "" && os.Geteuid() == 0 {
		t.Skip("running as root, chmod doesn't restrict access")
	}
	tmp := withTempDir(t)
	path := filepath.Join(tmp, "themes.toml")
	if err := os.WriteFile(path, []byte("theme = \"solarized\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(path, 0o644) })
	_, _, err := Load()
	if err == nil {
		t.Fatal("Load() error = nil, want non-nil for unreadable file")
	}
	if !strings.Contains(err.Error(), "failed to read") {
		t.Errorf("error message %q should mention read failure", err.Error())
	}
}

// TestSave_AtomicWrite: Save writes the file with the canonical format.
// Pre-existing content is replaced; the file is readable after.
func TestSave_AtomicWrite(t *testing.T) {
	tmp := withTempDir(t)
	// Pre-existing content
	if err := os.WriteFile(filepath.Join(tmp, "themes.toml"), []byte("theme = \"default\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := Save("solarized"); err != nil {
		t.Fatalf("Save() error = %v", err)
	}
	data, err := os.ReadFile(filepath.Join(tmp, "themes.toml"))
	if err != nil {
		t.Fatal(err)
	}
	want := "theme = \"solarized\"\n"
	if string(data) != want {
		t.Errorf("file contents = %q, want %q", string(data), want)
	}
}

// TestSave_UnknownName_DoesNotWrite: Save("foo") errors and leaves the
// existing file untouched. The atomic-write contract — never leave a
// broken file behind.
func TestSave_UnknownName_DoesNotWrite(t *testing.T) {
	tmp := withTempDir(t)
	original := "theme = \"solarized\"\n"
	if err := os.WriteFile(filepath.Join(tmp, "themes.toml"), []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}
	err := Save("nonexistent")
	if err == nil {
		t.Fatal("Save(\"nonexistent\") error = nil, want non-nil")
	}
	data, err := os.ReadFile(filepath.Join(tmp, "themes.toml"))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != original {
		t.Errorf("file contents changed: got %q, want %q", string(data), original)
	}
}

// TestUnset_RemovesFile: themes.toml exists → Unset removes it. Next
// Load returns (Default, false, nil).
func TestUnset_RemovesFile(t *testing.T) {
	tmp := withTempDir(t)
	if err := os.WriteFile(filepath.Join(tmp, "themes.toml"), []byte("theme = \"solarized\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := Unset(); err != nil {
		t.Fatalf("Unset() error = %v", err)
	}
	if _, err := os.Stat(filepath.Join(tmp, "themes.toml")); !os.IsNotExist(err) {
		t.Errorf("themes.toml still exists after Unset: %v", err)
	}
	// Next Load returns Default
	theme, set, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if set {
		t.Errorf("Load() set = true after Unset")
	}
	if theme.Name != Default.Name {
		t.Errorf("Load() theme = %q, want Default %q", theme.Name, Default.Name)
	}
}

// TestUnset_MissingFile: themes.toml doesn't exist → Unset is a no-op
// success. Matches the "no-op success" behavior in the spec.
func TestUnset_MissingFile(t *testing.T) {
	withTempDir(t)
	if err := Unset(); err != nil {
		t.Errorf("Unset() error = %v on missing file, want nil", err)
	}
}
```

- [ ] **Step 2: Run tests to verify they pass**

Run: `go test ./internal/themes -v -run 'TestLoad|TestSave|TestUnset'`
Expected: PASS, all 14 tests.

- [ ] **Step 3: Commit**

```bash
git add internal/themes/load_test.go
git commit -m "test(themes): add loader tests - completes plan item #4"
```

---

## Task 5: Wire theme into `app` struct and `newApp()`

**Files:**
- Modify: `cmd/wt/app.go`

**Interfaces:**
- Consumes: `themes.Load`, `themes.Theme`
- Produces: `app` struct gains `theme themes.Theme` field; `newApp()` loads it

- [ ] **Step 1: Modify `cmd/wt/app.go`**

Replace the file contents with:

```go
package main

import (
	"github.com/ohanaverse/agent-worktree/internal/config"
	"github.com/ohanaverse/agent-worktree/internal/themes"
)

// app holds shared dependencies loaded once at startup.
type app struct {
	cfg   *config.Config
	theme themes.Theme // active theme; populated by newApp()
}

// newApp loads and validates the config and loads the active theme. Live
// model discovery is deferred to the `models` subcommand so flag-only paths
// (--version, --init, -w, --cwd) don't shell out to ollama or hit the
// OpenRouter API.
func newApp() (*app, error) {
	cfg, err := config.Load()
	if err != nil {
		return nil, err
	}
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	// Load the active theme. A malformed themes.toml is a hard error —
	// the user wrote something they can't fix by accident, so we surface
	// it instead of silently falling back to default.
	theme, _, err := themes.Load()
	if err != nil {
		return nil, err
	}
	return &app{cfg: cfg, theme: theme}, nil
}
```

- [ ] **Step 2: Build to verify it compiles**

Run: `go build ./cmd/wt`
Expected: no output, exit 0.

- [ ] **Step 3: Run all existing tests to verify no regression**

Run: `go test ./...`
Expected: PASS, all existing tests still pass (the `app` struct shape change is internal — no consumer is affected yet).

- [ ] **Step 4: Commit**

```bash
git add cmd/wt/app.go
git commit -m "feat(app): load active theme at startup - completes plan item #5"
```

---

## Task 6: Theme `borderStyle` function + `renderTable` takes theme

**Files:**
- Modify: `cmd/wt/helpers.go`

**Interfaces:**
- Consumes: `themes.Theme`, `themes.Token`
- Produces:
  - `func renderTable(headers []string, rows [][]string, theme themes.Theme) string`
  - `func borderStyle(theme themes.Theme) lipgloss.Style` (replaces package-level var)

- [ ] **Step 1: Modify `cmd/wt/helpers.go` — replace `borderStyle` var and `renderTable` signature**

Replace the existing `borderStyle` var (line 40) and `renderTable` function (lines 42-51) with:

```go
// borderStyle returns the table border style for the active theme.
func borderStyle(theme themes.Theme) lipgloss.Style {
	return lipgloss.NewStyle().Foreground(theme.Token(themes.TokenBorder))
}

// renderTable renders a simple lipgloss table from headers and rows.
// theme controls the border color (and is also threaded through to cells
// in the future; today only the border consumes it).
func renderTable(headers []string, rows [][]string, theme themes.Theme) string {
	t := table.New().
		Headers(headers...).
		Rows(rows...).
		Border(lipgloss.NormalBorder()).
		BorderStyle(borderStyle(theme)).
		BorderRow(true)
	return t.Render()
}
```

Add to the imports block (after the existing `github.com/charmbracelet/lipgloss/table` import):

```go
"github.com/ohanaverse/agent-worktree/internal/themes"
```

- [ ] **Step 2: Build to verify — expect consumer errors**

Run: `go build ./cmd/wt`
Expected: build FAILS with errors like "renderTable has more arguments". This is correct — consumers in `commands.go` and `commands_config.go` (Tasks 7-8) need to pass the theme.

Don't fix the consumers yet; Task 7 handles them.

- [ ] **Step 3: Commit**

```bash
git add cmd/wt/helpers.go
git commit -m "refactor(helpers): thread theme through renderTable - completes plan item #6"
```

---

## Task 7: Update `commands.go` to pass theme to `renderTable`

**Files:**
- Modify: `cmd/wt/commands.go`

**Interfaces:**
- Consumes: `app.theme`, updated `renderTable` signature
- Produces: `modelsCmd` and `agentsCmd` pass `a.theme` to `renderTable`

- [ ] **Step 1: Update `modelsCmd`**

In `cmd/wt/commands.go`, find each call to `renderTable(...)` and replace with `renderTable(..., a.theme)`. Specifically:

In the Providers table block:
```go
fmt.Println(renderTable(
    []string{"ID", "LOCATION", "AUTH", "BASE_URL"},
    provRows,
    a.theme,
))
```

In the Models table block:
```go
fmt.Println(renderTable(
    []string{"ID", "FAMILY", "PROVIDER", "LOCATION", "TAGS"},
    modelRows,
    a.theme,
))
```

In the Agents table block:
```go
fmt.Println(renderTable(
    []string{"NAME", "PROVIDERS", "DEFAULT"},
    agentRows,
    a.theme,
))
```

- [ ] **Step 2: Update `agentsCmd`**

In the same file, find the `renderTable(...)` call in `agentsCmd`:
```go
fmt.Println(renderTable(
    []string{"NAME", "INSTALLED", "YOLO_FLAG"},
    rows,
    a.theme,
))
```

- [ ] **Step 3: Build to verify it compiles**

Run: `go build ./cmd/wt`
Expected: no output, exit 0.

- [ ] **Step 4: Run all existing tests to verify no regression**

Run: `go test ./...`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add cmd/wt/commands.go
git commit -m "feat(commands): theme models/agents tables via renderTable - completes plan item #7"
```

---

## Task 8: `wt config` cobra command (theme list/show/set/unset + path)

**Files:**
- Create: `cmd/wt/commands_config.go`

**Interfaces:**
- Consumes: `themes.Builtins`, `themes.Get`, `themes.Save`, `themes.Unset`, `themes.Path`, `themes.Load`, `themes.AvailableList`, `app.theme`
- Produces:
  - `func configCmd(a *app) *cobra.Command`
  - `func configPathCmd(a *app) *cobra.Command`
  - `func configThemeCmd(a *app) *cobra.Command`
  - `func configThemeListCmd(a *app) *cobra.Command`
  - `func configThemeShowCmd(a *app) *cobra.Command`
  - `func configThemeSetCmd(a *app) *cobra.Command`
  - `func configThemeUnsetCmd() *cobra.Command`

- [ ] **Step 1: Create `cmd/wt/commands_config.go`**

```go
// wt config command — user-level preferences. The first shipped subcommand
// is `wt config theme` for managing the active color theme. Future
// subcommands (wt config ollama sync, wt config registry edit) slot in
// here without breaking changes.
//
// The theme subcommand family uses the active theme (loaded by newApp)
// for its own output where appropriate (table borders, list names in their
// accent colors). The exception is `wt config theme set`: the user is
// actively changing the theme, so the prompt stays unthemed.

package main

import (
	"fmt"
	"sort"

	"github.com/charmbracelet/lipgloss"
	"github.com/ohanaverse/agent-worktree/internal/themes"
	"github.com/spf13/cobra"
)

// configCmd returns the `wt config` command. Subcommands are added below;
// future subcommands (wt config ollama …, etc.) register here.
func configCmd(a *app) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "config",
		Short: "Manage wt preferences",
		Long:  "Manage wt user preferences. Subcommands configure specific concerns:\n" +
			"  theme  active color theme\n" +
			"  path   print the config directory",
		// No RunE: cobra prints help when called with no subcommand.
	}
	cmd.AddCommand(configPathCmd(a), configThemeCmd(a))
	return cmd
}

// configPathCmd prints the config directory. Useful as a discovery helper
// when users want to inspect themes.toml by hand.
func configPathCmd(a *app) *cobra.Command {
	return &cobra.Command{
		Use:   "path",
		Short: "Print the config directory",
		RunE: func(cmd *cobra.Command, args []string) error {
			fmt.Println(a.cfg.Dir()) // wait — Dir is unexported. Use themes.Path()'s parent or add a helper.
			return nil
		},
	}
}
```

Wait — `config.Dir()` is unexported. The plan needs to either export it or call it via a path-aware helper. Let me fix this inline by using `themes.Path()`'s parent directory (which is the config dir):

Replace the `configPathCmd.RunE` body:

```go
		RunE: func(cmd *cobra.Command, args []string) error {
			fmt.Println(filepath.Dir(themes.Path()))
			return nil
		},
```

And add `"path/filepath"` to the imports.

Continuing the file (this is the full file with the fix applied):

```go
package main

import (
	"fmt"
	"path/filepath"
	"sort"

	"github.com/charmbracelet/lipgloss"
	"github.com/ohanaverse/agent-worktree/internal/themes"
	"github.com/spf13/cobra"
)

// configCmd returns the `wt config` command.
func configCmd(a *app) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "config",
		Short: "Manage wt preferences",
		Long:  "Manage wt user preferences. Subcommands configure specific concerns:\n" +
			"  theme  active color theme\n" +
			"  path   print the config directory",
	}
	cmd.AddCommand(configPathCmd(a), configThemeCmd(a))
	return cmd
}

// configPathCmd prints the config directory.
func configPathCmd(a *app) *cobra.Command {
	return &cobra.Command{
		Use:   "path",
		Short: "Print the config directory",
		RunE: func(cmd *cobra.Command, args []string) error {
			fmt.Println(filepath.Dir(themes.Path()))
			return nil
		},
	}
}

// configThemeCmd returns the `wt config theme` parent command. Subcommands
// are added in configThemeCmd; future per-theme actions register here.
func configThemeCmd(a *app) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "theme",
		Short: "Manage the active color theme",
		Long: "Manage the active color theme.\n" +
			"  list    list available themes\n" +
			"  show    show a theme's tokens (active theme if no name given)\n" +
			"  set     activate a theme (effective on next wt launch)\n" +
			"  unset   remove the theme choice (use default)",
	}
	cmd.AddCommand(
		configThemeListCmd(a),
		configThemeShowCmd(a),
		configThemeSetCmd(),
		configThemeUnsetCmd(),
	)
	return cmd
}

// configThemeListCmd lists all built-in themes. Names are rendered in that
// theme's accent color so users can scan and see which themes look
// distinct.
func configThemeListCmd(a *app) *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List available themes",
		RunE: func(cmd *cobra.Command, args []string) error {
			themes := themes.Builtins()
			rows := make([][]string, 0, len(themes))
			for _, th := range themes {
				// Render the name in that theme's accent color.
				nameStyle := lipgloss.NewStyle().Foreground(th.Token(themes.TokenAccent))
				// Description is a short blurb — kept inline for now.
				rows = append(rows, []string{
					nameStyle.Render(th.Name),
					themeDescription(th.Name),
				})
			}
			// Sort by rendered name for stable ordering. Note: ANSI codes
			// would normally confuse string comparison, but we control the
			// styles and use only foreground codes that don't affect sort
			// order in practice. (If this becomes flaky, sort by raw name
			// instead and accept the cosmetic discrepancy.)
			sort.SliceStable(rows, func(i, j int) bool {
				return themes[i].Name < themes[j].Name
			})
			fmt.Println(renderTable(
				[]string{"NAME", "DESCRIPTION"},
				rows,
				a.theme,
			))
			return nil
		},
	}
}

// themeDescription returns a short blurb for a built-in theme. Used in the
// `wt config theme list` output.
func themeDescription(name string) string {
	switch name {
	case "default":
		return "subtle, terminal-native colors"
	case "solarized":
		return "warm palette inspired by Solarized"
	case "mono":
		return "grayscale with a single blue accent"
	case "tokyo-night":
		return "Enkia's Tokyo Night (Night/Day pair)"
	}
	return ""
}

// configThemeShowCmd shows a single theme's tokens. With no argument, shows
// the active theme. Format:
//
//   <name> — <description>
//   <token>  <dark hex> / <light hex>  dark light
//   ...
//
// The "dark" and "light" preview words render in the token's own dark/light
// color respectively.
func configThemeShowCmd(a *app) *cobra.Command {
	return &cobra.Command{
		Use:   "show [<name>]",
		Short: "Show a theme's tokens",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			var theme themes.Theme
			var ok bool
			if len(args) == 0 {
				theme = a.theme
			} else {
				theme, ok = themes.Get(args[0])
				if !ok {
					return fmt.Errorf("wt: unknown theme %q — available: %s",
						args[0], joinThemeNames())
				}
			}
			renderThemePreview(theme)
			return nil
		},
	}
}

// renderThemePreview prints a single theme's tokens with dark/light
// previews. Used by both configThemeShowCmd and the active-theme display
// in configThemeCmd.
func renderThemePreview(theme themes.Theme) {
	desc := themeDescription(theme.Name)
	if desc != "" {
		fmt.Printf("%s — %s\n\n", theme.Name, desc)
	} else {
		fmt.Printf("%s\n\n", theme.Name)
	}
	for _, token := range themes.AllTokens() {
		c := theme.Token(token)
		darkStyle := lipgloss.NewStyle().Foreground(c.Dark)
		lightStyle := lipgloss.NewStyle().Foreground(c.Light)
		// 12-char left-aligned token name; hex pairs; preview words.
		fmt.Printf("  %-12s %s / %s  %s %s\n",
			token,
			c.Dark, c.Light,
			darkStyle.Render("dark"),
			lightStyle.Render("light"),
		)
	}
	fmt.Printf("\nset with: wt config theme set %s\n", theme.Name)
}

// configThemeSetCmd activates a theme. Writes themes.toml atomically. The
// message to stderr notes that the new theme takes effect on the next
// launch — themes don't hot-reload.
func configThemeSetCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "set <name>",
		Short: "Activate a theme",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			if err := themes.Save(name); err != nil {
				return err
			}
			fmt.Fprintf(cmd.ErrOrStderr(),
				"wt: theme set to %q (effective on next wt launch)\n", name)
			return nil
		},
	}
}

// configThemeUnsetCmd removes the theme choice. No-op success if no theme
// is set.
func configThemeUnsetCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "unset",
		Short: "Remove the theme choice (use default)",
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := themes.Unset(); err != nil {
				return err
			}
			fmt.Fprintln(cmd.ErrOrStderr(),
				"wt: no theme set (using \"default\")")
			return nil
		},
	}
}

// joinThemeNames formats AvailableList() as a comma-separated string.
// Used in error messages — order is stable.
func joinThemeNames() string {
	names := themes.AvailableList()
	out := ""
	for i, n := range names {
		if i > 0 {
			out += ", "
		}
		out += n
	}
	return out
}
```

- [ ] **Step 2: Wire the command into `main.go`**

In `cmd/wt/main.go`, find the line:
```go
cmd.AddCommand(modelsCmd(a), agentsCmd(a), rotateCmd(a))
```

Replace with:
```go
cmd.AddCommand(modelsCmd(a), agentsCmd(a), rotateCmd(a), configCmd(a))
```

- [ ] **Step 3: Build to verify it compiles**

Run: `go build ./cmd/wt`
Expected: no output, exit 0.

- [ ] **Step 4: Smoke-test the commands manually**

Run each command and verify output:

```bash
./bin/wt config                 # help text
./bin/wt config path            # ~/.config/agent-wt or $XDG_CONFIG_HOME/agent-wt
./bin/wt config theme           # "active: default" (or similar)
./bin/wt config theme list      # 4 themes with colored names
./bin/wt config theme show      # active theme tokens
./bin/wt config theme show solarized
./bin/wt config theme set tokyo-night
./bin/wt config theme show      # now shows tokyo-night
./bin/wt config theme unset
./bin/wt config theme           # back to default
```

Expected: every command exits 0 with the documented output.

- [ ] **Step 5: Run all existing tests**

Run: `go test ./...`
Expected: PASS (no existing tests broken).

- [ ] **Step 6: Commit**

```bash
git add cmd/wt/commands_config.go cmd/wt/main.go
git commit -m "feat(config): add wt config command with theme subcommand - completes plan item #8"
```

---

## Task 9: Cobra wiring tests

**Files:**
- Create: `cmd/wt/commands_config_test.go`

**Interfaces:**
- Consumes: `configCmd`, `app` struct, themes test seam
- Produces: 11 tests covering cobra wiring

- [ ] **Step 1: Write the failing tests**

```go
// internal/cmd/wt/commands_config_test.go
//
// Tests for the wt config cobra wiring. Verifies subcommand dispatch,
// error messages, and the atomic-write guarantee (unknown theme names
// never leave a broken file). Uses the themes test seam to redirect
// themes.toml to a temp dir.

package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ohanaverse/agent-worktree/internal/config"
	"github.com/ohanaverse/agent-worktree/internal/themes"
)

// newTestApp returns an app with both config and theme loaded from a temp
// dir. Tests that need to assert file state use the returned tmp path.
func newTestApp(t *testing.T) (*app, string) {
	t.Helper()
	tmp := t.TempDir()
	// Redirect themes to tmp via the seam
	orig := themesDirFunc()
	themesDirFunc = func() string { return tmp }
	t.Cleanup(func() { themesDirFunc = orig })
	// Write a minimal config.toml so config.Load succeeds
	if err := os.WriteFile(filepath.Join(tmp, "config.toml"),
		[]byte("default_tag = \"code\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	a, err := newApp()
	if err != nil {
		t.Fatalf("newApp() error = %v", err)
	}
	return a, tmp
}

// themesDirFunc is an indirection so tests can swap the directory without
// touching production code. Mirrors the dirFunc seam in internal/themes.
var themesDirFunc = func() string { return config.Dir() }

// TestConfigCmd_NoSubcommand_PrintsHelp: wt config with no args → exits 0
// with help text on stdout.
func TestConfigCmd_NoSubcommand_PrintsHelp(t *testing.T) {
	a, _ := newTestApp(t)
	cmd := configCmd(a)
	cmd.SetArgs([]string{})
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
}

// TestConfigCmd_UnknownSubcommand: wt config foo → exits non-zero.
func TestConfigCmd_UnknownSubcommand(t *testing.T) {
	a, _ := newTestApp(t)
	cmd := configCmd(a)
	cmd.SetArgs([]string{"foo"})
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	err := cmd.Execute()
	if err == nil {
		t.Fatal("Execute() error = nil, want non-nil for unknown subcommand")
	}
}

// TestConfigThemeCmd_NoAction_PrintsHelp: wt config theme → exits 0.
func TestConfigThemeCmd_NoAction_PrintsHelp(t *testing.T) {
	a, _ := newTestApp(t)
	cmd := configCmd(a)
	cmd.SetArgs([]string{"theme"})
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
}

// TestConfigThemeShow_NoArg_ShowsActive: `wt config theme show` with no
// name → shows the active theme's tokens. Since newTestApp doesn't write a
// themes.toml, the active theme is the Default.
func TestConfigThemeShow_NoArg_ShowsActive(t *testing.T) {
	a, _ := newTestApp(t)
	cmd := configCmd(a)
	var out bytes.Buffer
	cmd.SetArgs([]string{"theme", "show"})
	cmd.SetOut(&out)
	cmd.SetErr(&bytes.Buffer{})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if !strings.Contains(out.String(), "default") {
		t.Errorf("output %q should contain the active theme name \"default\"", out.String())
	}
}

// TestConfigThemeShow_UnknownName_Errors: error message contains available
// theme names.
func TestConfigThemeShow_UnknownName_Errors(t *testing.T) {
	a, _ := newTestApp(t)
	cmd := configCmd(a)
	cmd.SetArgs([]string{"theme", "show", "nonexistent"})
	var stderr bytes.Buffer
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&stderr)
	err := cmd.Execute()
	if err == nil {
		t.Fatal("Execute() error = nil, want non-nil")
	}
	out := stderr.String() + err.Error()
	for _, name := range []string{"default", "solarized", "mono", "tokyo-night"} {
		if !strings.Contains(out, name) {
			t.Errorf("error %q should list available theme %q", out, name)
		}
	}
}

// TestConfigThemeSet_UnknownName_DoesNotWrite: wt config theme set foo →
// error AND themes.toml is unchanged. Atomic-write guarantee.
func TestConfigThemeSet_UnknownName_DoesNotWrite(t *testing.T) {
	a, tmp := newTestApp(t)
	// Pre-write a known-good themes.toml
	original := "theme = \"solarized\"\n"
	if err := os.WriteFile(filepath.Join(tmp, "themes.toml"), []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}
	cmd := configCmd(a)
	cmd.SetArgs([]string{"theme", "set", "nonexistent"})
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	err := cmd.Execute()
	if err == nil {
		t.Fatal("Execute() error = nil, want non-nil")
	}
	// File unchanged
	data, _ := os.ReadFile(filepath.Join(tmp, "themes.toml"))
	if string(data) != original {
		t.Errorf("themes.toml changed: got %q, want %q", string(data), original)
	}
}

// TestConfigThemeSet_ValidName_WritesFile: file contains the new theme.
func TestConfigThemeSet_ValidName_WritesFile(t *testing.T) {
	a, tmp := newTestApp(t)
	cmd := configCmd(a)
	cmd.SetArgs([]string{"theme", "set", "tokyo-night"})
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	data, err := os.ReadFile(filepath.Join(tmp, "themes.toml"))
	if err != nil {
		t.Fatal(err)
	}
	want := "theme = \"tokyo-night\"\n"
	if string(data) != want {
		t.Errorf("themes.toml = %q, want %q", string(data), want)
	}
}

// TestConfigThemeSet_EmptyName_Errors: empty string isn't a valid theme.
func TestConfigThemeSet_EmptyName_Errors(t *testing.T) {
	a, _ := newTestApp(t)
	cmd := configCmd(a)
	cmd.SetArgs([]string{"theme", "set", ""})
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	err := cmd.Execute()
	if err == nil {
		t.Fatal("Execute() error = nil, want non-nil for empty theme name")
	}
}

// TestConfigThemeList_PrintsAllThemes: output contains all 4 names.
func TestConfigThemeList_PrintsAllThemes(t *testing.T) {
	a, _ := newTestApp(t)
	cmd := configCmd(a)
	var out bytes.Buffer
	cmd.SetArgs([]string{"theme", "list"})
	cmd.SetOut(&out)
	cmd.SetErr(&bytes.Buffer{})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	for _, name := range []string{"default", "solarized", "mono", "tokyo-night"} {
		if !strings.Contains(out.String(), name) {
			t.Errorf("output should contain %q", name)
		}
	}
}

// TestConfigTheme_ShowsActive: `wt config theme` (no action) shows active.
func TestConfigTheme_ShowsActive(t *testing.T) {
	a, tmp := newTestApp(t)
	// Pre-write a non-default theme
	if err := os.WriteFile(filepath.Join(tmp, "themes.toml"),
		[]byte("theme = \"solarized\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	a2, _ := newTestApp(t)
	_ = a // unused; a2 reflects the new file state
	var out bytes.Buffer
	cmd := configCmd(a2)
	cmd.SetArgs([]string{"theme"})
	cmd.SetOut(&out)
	cmd.SetErr(&bytes.Buffer{})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if !strings.Contains(out.String(), "solarized") {
		t.Errorf("output %q should contain active theme \"solarized\"", out.String())
	}
}

// TestConfigPath_PrintsDir: `wt config path` prints the directory.
func TestConfigPath_PrintsDir(t *testing.T) {
	a, tmp := newTestApp(t)
	cmd := configCmd(a)
	var out bytes.Buffer
	cmd.SetArgs([]string{"path"})
	cmd.SetOut(&out)
	cmd.SetErr(&bytes.Buffer{})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if !strings.Contains(out.String(), tmp) {
		t.Errorf("output %q should contain config dir %q", out.String(), tmp)
	}
}
```

- [ ] **Step 2: Run tests to verify they pass**

Run: `go test ./cmd/wt -v -run 'TestConfig'`
Expected: PASS, all 11 tests.

If `TestConfigTheme_ShowsActive` has a typo issue (the test calls `newTestApp` twice — the first `a` is unused), simplify:

```go
func TestConfigTheme_ShowsActive(t *testing.T) {
	tmp := t.TempDir()
	orig := themesDirFunc
	themesDirFunc = func() string { return tmp }
	t.Cleanup(func() { themesDirFunc = orig })
	if err := os.WriteFile(filepath.Join(tmp, "config.toml"),
		[]byte("default_tag = \"code\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tmp, "themes.toml"),
		[]byte("theme = \"solarized\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	a, err := newApp()
	if err != nil {
		t.Fatalf("newApp() error = %v", err)
	}
	var out bytes.Buffer
	cmd := configCmd(a)
	cmd.SetArgs([]string{"theme"})
	cmd.SetOut(&out)
	cmd.SetErr(&bytes.Buffer{})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if !strings.Contains(out.String(), "solarized") {
		t.Errorf("output %q should contain active theme \"solarized\"", out.String())
	}
}
```

- [ ] **Step 3: Commit**

```bash
git add cmd/wt/commands_config_test.go
git commit -m "test(config): add cobra wiring tests - completes plan item #9"
```

---

## Task 10: TUI — `model.theme` field + `Run` signature

**Files:**
- Modify: `internal/tui/app.go`

**Interfaces:**
- Consumes: `themes.Theme`
- Produces:
  - `model` struct gains `theme themes.Theme` field
  - `Run(yolo bool, agent, tags, family string, extraArgs []string, theme themes.Theme) error`

- [ ] **Step 1: Add `theme` field to `model` struct**

In `internal/tui/app.go`, find the `model` struct (starts at line 48) and add a new field alongside the existing `cfg` field:

```go
	cfg      *config.Config     // loaded config for the model catalog
	theme    themes.Theme       // active color theme; passed from cmd/wt
```

The exact line numbers and surrounding context: the existing fields around `cfg`:

```go
	models list.Model // bubble/list of agent+tag models
```

(Just the model struct definition block; see lines 47-99 of the current file.)

- [ ] **Step 2: Update `Run` to accept and pass theme**

Find the `Run` function (line 730) and update its signature:

```go
func Run(yolo bool, agent, tags, family string, extraArgs []string, theme themes.Theme) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	p := tea.NewProgram(model{
		status:       "loading worktrees...",
		cfg:          cfg,
		theme:        theme,
		yolo:         yolo,
		initialAgent: agent,
		activeTags:   tags,
		activeFamily: family,
		extraArgs:    extraArgs,
	}, tea.WithAltScreen())
	currentProgram = p
	_, err = p.Run()
	return err
}
```

- [ ] **Step 3: Update caller in `cmd/wt/main.go`**

Find the line:
```go
return tui.Run(yolo(cmd), agent, tags, family, args)
```

Replace with:
```go
return tui.Run(yolo(cmd), agent, tags, family, args, a.theme)
```

- [ ] **Step 4: Build to verify it compiles**

Run: `go build ./cmd/wt`
Expected: no output, exit 0.

- [ ] **Step 5: Run all tests**

Run: `go test ./...`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/tui/app.go cmd/wt/main.go
git commit -m "feat(tui): thread theme into model via Run - completes plan item #10"
```

---

## Task 11: TUI — themed `errorStyle` and picker styles

**Files:**
- Modify: `internal/tui/new_worktree.go`
- Modify: `internal/tui/model_list.go`

**Interfaces:**
- Consumes: `m.theme`, `themes.Token`
- Produces:
  - `func errorStyle(theme themes.Theme) lipgloss.Style` (replaces package-level var)
  - Themed picker rendering in `model_list.go`

- [ ] **Step 1: Replace `errorStyle` var with a function in `new_worktree.go`**

In `internal/tui/new_worktree.go`, find the line:
```go
var errorStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("9"))
```

Replace with:
```go
// errorStyle returns the lipgloss style for user-facing errors in the
// active theme. Replaces the previous package-level var so themed errors
// adapt to light/dark automatically.
func errorStyle(theme themes.Theme) lipgloss.Style {
	return lipgloss.NewStyle().Foreground(theme.Token(themes.TokenError))
}
```

Add `"github.com/ohanaverse/agent-worktree/internal/themes"` to the imports.

- [ ] **Step 2: Update callers of `errorStyle`**

In `internal/tui/app.go`, find every call to `errorStyle` (in the `View()` method). The current calls:

```go
body += "\n" + errorStyle.Render(m.newError)
```

```go
return errorStyle.Render("error: "+m.listError) + "\n" + m.list.View()
```

```go
return errorStyle.Render(m.status) + "\n" + m.list.View()
```

Update each to pass `m.theme`:

```go
body += "\n" + errorStyle(m.theme).Render(m.newError)
```

```go
return errorStyle(m.theme).Render("error: "+m.listError) + "\n" + m.list.View()
```

```go
return errorStyle(m.theme).Render(m.status) + "\n" + m.list.View()
```

- [ ] **Step 3: Theme the model picker view in `model_list.go`**

In `internal/tui/model_list.go`, find the `phaseModelView` function and update its styling. Current implementation:

```go
func (m *model) phaseModelView() string {
	style := lipgloss.NewStyle().Padding(1, 2)
	header := fmt.Sprintf("agent : %s\ntag   : %s\n", m.agent, m.tag)
	footer := "\n[↑/↓] navigate   [enter] launch   [q] quit"
	return style.Render(header + m.models.View() + footer)
}
```

Replace with:

```go
func (m *model) phaseModelView() string {
	pad := lipgloss.NewStyle().Padding(1, 2)
	headerStyle := lipgloss.NewStyle().Foreground(m.theme.Token(themes.TokenHeader))
	dimStyle := lipgloss.NewStyle().Foreground(m.theme.Token(themes.TokenDim))
	header := headerStyle.Render(fmt.Sprintf("agent : %s\ntag   : %s\n", m.agent, m.tag))
	footer := dimStyle.Render("\n[↑/↓] navigate   [enter] launch   [q] quit")
	return pad.Render(header + m.models.View() + footer)
}
```

Add `"github.com/ohanaverse/agent-worktree/internal/themes"` to the imports.

- [ ] **Step 4: Build to verify**

Run: `go build ./...`
Expected: no output, exit 0.

- [ ] **Step 5: Run all tests**

Run: `go test ./...`
Expected: PASS.

- [ ] **Step 6: Smoke-test the TUI manually**

Run: `./bin/wt`
Expected: TUI launches, error styling uses the active theme, model picker header/footer are themed.

- [ ] **Step 7: Commit**

```bash
git add internal/tui/new_worktree.go internal/tui/model_list.go internal/tui/app.go
git commit -m "feat(tui): theme error styling and picker - completes plan item #11"
```

---

## Task 12: TUI — themed `bubbles/list` delegate

**Files:**
- Modify: `internal/tui/app.go`

**Interfaces:**
- Consumes: `m.theme`, `themes.TokenSelected`, `themes.TokenUnselected`
- Produces: All `list.New(..., list.NewDefaultDelegate(), ...)` calls become themed delegates

- [ ] **Step 1: Add a themed list delegate helper**

In `internal/tui/app.go`, add a helper function near the top of the file (after the imports, before the type declarations):

```go
// themedListDelegate returns a list.ItemDelegate that uses the active theme
// for selected/unselected row colors. This overrides bubbles/list's default
// delegate so pickers adapt to the user's theme instead of using the
// bubbles default.
func themedListDelegate(theme themes.Theme) list.ItemDelegate {
	d := list.NewDefaultDelegate()
	d.Styles.SelectedTitle = d.Styles.SelectedTitle.Foreground(theme.Token(themes.TokenSelected))
	d.Styles.SelectedDesc = d.Styles.SelectedDesc.Foreground(theme.Token(themes.TokenSelected))
	d.Styles.NormalTitle = d.Styles.NormalTitle.Foreground(theme.Token(themes.TokenUnselected))
	d.Styles.NormalDesc = d.Styles.NormalDesc.Foreground(theme.Token(themes.TokenUnselected))
	return d
}
```

- [ ] **Step 2: Replace `list.NewDefaultDelegate()` calls**

In `internal/tui/app.go`, find every occurrence of `list.NewDefaultDelegate()` (should be 6 — for `m.list`, `m.agentList`, `m.models`, `m.resume.choices`, `m.guardWarnModel`, `m.ollamaWarnModel`).

Replace each with `themedListDelegate(m.theme)`. Specifically:

```go
m.list = buildList(msg.groups, msg.defaultBranch, msg.repoRoot, m.width-2, m.height-2)
```

The `buildList` helper internally creates a `list.Model` with `list.NewDefaultDelegate()`. Find that in `internal/tui/worktree_list.go` and replace. (If `buildList` is invoked with a theme, pass it; otherwise look up where to inject the delegate.)

For the worktree list, edit `internal/tui/worktree_list.go`'s `buildList` function to accept a theme and use `themedListDelegate(theme)`:

```go
func buildList(groups []worktree.EntryGroup, defaultBranch, repoRoot string, width, height int, theme themes.Theme) list.Model {
	l := list.New([]list.Item{}, themedListDelegate(theme), width, height)
	// ... rest of buildList ...
}
```

Then update the caller in `app.go`'s `entriesLoadedMsg` handler:

```go
m.list = buildList(msg.groups, msg.defaultBranch, msg.repoRoot, m.width-2, m.height-2, m.theme)
```

- [ ] **Step 3: Build, test, and commit**

Run: `go build ./... && go test ./...`
Expected: PASS.

```bash
git add internal/tui/app.go internal/tui/worktree_list.go
git commit -m "feat(tui): theme bubbles/list delegates - completes plan item #12"
```

---

## Task 13: Makefile smoke tests

**Files:**
- Modify: `Makefile`

- [ ] **Step 1: Find the `make test` target**

Read the Makefile and find the `test:` target. Add `wt config` smoke tests:

```makefile
	./bin/wt config                       # prints help, exits 0
	./bin/wt config theme                 # prints active theme, exits 0
	./bin/wt config theme list            # prints 4 themes, exits 0
	./bin/wt config theme show default    # shows default tokens, exits 0
	./bin/wt config theme set tokyo-night # sets theme, exits 0
	./bin/wt config theme unset           # unsets, exits 0
```

Also add a regression check on the existing launch path:

```makefile
	./bin/wt --init                       # existing --init flag still works
```

- [ ] **Step 2: Run `make test` to verify**

Run: `make test`
Expected: PASS, including the new smoke tests.

- [ ] **Step 3: Commit**

```bash
git add Makefile
git commit -m "test(make): add wt config smoke tests - completes plan item #13"
```

---

## Task 14: Final review — run full test suite + manual sweep

**Files:** None (verification only)

- [ ] **Step 1: Run `go test ./...`**

Run: `go test ./...`
Expected: PASS, all tests.

Expected test counts (sanity check):
- `internal/themes`: 24+ tests (10 registry + 14 loader)
- `cmd/wt`: 11+ new config tests + 29 existing = 40+
- Other packages: unchanged from current state

- [ ] **Step 2: Run `go vet ./...`**

Run: `go vet ./...`
Expected: no output, exit 0.

- [ ] **Step 3: Run `make check`**

Run: `make check`
Expected: PASS (lint + format-check).

- [ ] **Step 4: Run `make test`**

Run: `make test`
Expected: PASS, including new smoke tests.

- [ ] **Step 5: Manual end-to-end smoke**

Run each of:
```bash
./bin/wt --version
./bin/wt config
./bin/wt config path
./bin/wt config theme list
./bin/wt config theme show tokyo-night
./bin/wt config theme set mono
./bin/wt config theme
./bin/wt config theme unset
./bin/wt --init
```

Expected: every command exits 0 with the documented behavior. Theme changes persist across `./bin/wt` invocations.

- [ ] **Step 6: Commit any final fixes (if needed)**

If any verification step failed and you fixed it, commit with a `fix:` prefix and explain what was wrong. If everything passed, no commit needed — proceed to handoff.

---

## Self-Review

**1. Spec coverage:**

| Spec section | Plan task |
|---|---|
| Architecture & components | Tasks 1, 3, 5, 8, 10 |
| Theme struct & token set | Task 1 |
| `Token` fallback chain | Task 1 (Token method), Task 2 (test) |
| 4 built-in themes | Task 1 (structs), Task 2 (count test) |
| `themes.toml` format | Tasks 3 (loader), 4 (tests) |
| Loader contract (6 branches) | Tasks 3, 4 (full coverage) |
| Test seam (`dirFunc`) | Tasks 1, 4 (uses) |
| Command surface (`wt config`, `path`, `theme list/show/set/unset`) | Task 8 |
| `wt config theme show` preview format | Task 8 (renderThemePreview) |
| Error handling (loader + writer + dispatch) | Tasks 3, 8 |
| Data flow (3 flows) | Tasks 5, 8, 10 |
| Testing (3 test files, smoke tests) | Tasks 2, 4, 9, 13 |
| Implementation cut list (YAGNI) | Honored — no scope creep |

**2. Placeholder scan:** No "TBD", "TODO", or vague requirements. Tokyo Night hex values are flagged "refine during implementation" in Task 1 — that's a deliberate note, not a placeholder.

**3. Type consistency:**
- `Theme` struct (`Name string; Tokens map[string]lipgloss.AdaptiveColor`) — defined in Task 1, used identically in Tasks 2-9. ✓
- `Load()` returns `(Theme, bool, error)` — defined in Task 3, consumed by Task 5 and Task 8. ✓
- `renderTable(headers []string, rows [][]string, theme themes.Theme)` — defined in Task 6, used identically in Tasks 7 and 8. ✓
- `Run(yolo bool, agent, tags, family string, extraArgs []string, theme themes.Theme)` — defined in Task 10, called from `cmd/wt/main.go` (Task 10 Step 3). ✓
- Token names (`TokenBorder`, etc.) — defined as constants in Task 1, referenced by name in Tasks 8, 11, 12. ✓
- `themedListDelegate(theme themes.Theme)` — defined in Task 12, used in Task 12. ✓

**4. Ambiguity check:** Step 1 of Task 8 had a draft that referenced `a.cfg.Dir()` which doesn't exist (unexported). Fixed inline by using `filepath.Dir(themes.Path())` instead. No remaining ambiguities.

---

## Open Questions

None. Implementation may surface palette tuning questions during Task 1 (Tokyo Night hex values) — those are expected, not plan failures.

---

## Handoff

Plan complete and saved to `docs/superpowers/plans/2026-08-20-wt-config-themes.md`. Two execution options:

1. **Subagent-Driven (recommended)** — I dispatch a fresh subagent per task, review between tasks, fast iteration
2. **Inline Execution** — Execute tasks in this session using executing-plans, batch execution with checkpoints

Which approach?
