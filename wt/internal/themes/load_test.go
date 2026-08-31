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
		"theme = \"solarized", // unclosed quote
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

// TestLoad_PermissionDenied: chmod 0000 → error. Skip when running as root
// since chmod doesn't restrict root.
func TestLoad_PermissionDenied(t *testing.T) {
	if os.Geteuid() == 0 {
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
	t.Cleanup(func() { _ = os.Chmod(path, 0o644) })
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
