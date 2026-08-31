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
	"github.com/ohanaverse/local-ai-setup/wt/internal/config"
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

// Names returns the available theme names as a comma-separated string, the
// single source of truth for the "available: …" list shown in error messages
// and `wt config theme` output.
func Names() string {
	return strings.Join(AvailableList(), ", ")
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

// DirFuncForTest returns the current directory function. Test-only helper
// for callers outside this package that need to override the dirFunc seam.
func DirFuncForTest() func() string { return dirFunc }

// SetDirFuncForTest replaces the directory function for testing. Caller is
// responsible for restoring the original via the value returned by
// DirFuncForTest. Test-only.
func SetDirFuncForTest(f func() string) { dirFunc = f }
