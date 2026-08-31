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
