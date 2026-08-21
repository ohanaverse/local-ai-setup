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
	"strings"

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

	if err := checkDuplicateThemeKey(data); err != nil {
		return Default, false, err
	}

	var tf themesFile
	if _, err := toml.Decode(string(data), &tf); err != nil {
		return Default, false, fmt.Errorf("themes.toml is malformed: %w", err)
	}

	if tf.Theme == "" {
		return Default, false, &ThemeNameError{
			msg: fmt.Sprintf("theme name in themes.toml is empty — available: %s",
				joinNames(AvailableList())),
		}
	}

	theme, ok := Get(tf.Theme)
	if !ok {
		return Default, false, &ThemeNameError{
			msg: fmt.Sprintf("unknown theme %q in themes.toml — available: %s",
				tf.Theme, joinNames(AvailableList())),
		}
	}
	return theme, true, nil
}

// ThemeNameError is returned by Load when the theme name in themes.toml is
// empty or unknown. The launcher treats this as a soft error — it falls back
// to Default so the user can still run `wt config theme set`/`unset` to
// repair the file. Use IsThemeNameError to test for this error type.
type ThemeNameError struct{ msg string }

func (e *ThemeNameError) Error() string { return e.msg }

// IsThemeNameError reports whether err is (or wraps) a ThemeNameError.
func IsThemeNameError(err error) bool {
	var t *ThemeNameError
	return errors.As(err, &t)
}

// checkDuplicateThemeKey rejects files with multiple `theme` keys.
// BurntSushi/toml silently deduplicates duplicates (keeps last), but we
// want to surface them so a script bug can't produce two competing values.
// Naive scan: count lines that look like `theme = ...`. False positives
// (a `theme` token inside a quoted string) are vanishingly unlikely for a
// one-key file.
func checkDuplicateThemeKey(data []byte) error {
	count := 0
	for i := 0; i < len(data); {
		j := i
		for j < len(data) && (data[j] == ' ' || data[j] == '\t') {
			j++
		}
		if j+5 <= len(data) && string(data[j:j+5]) == "theme" {
			k := j + 5
			for k < len(data) && (data[k] == ' ' || data[k] == '\t') {
				k++
			}
			if k < len(data) && data[k] == '=' {
				count++
				if count > 1 {
					return errors.New(`themes.toml has duplicate "theme" key`)
				}
				j = k + 1
			} else {
				j = k
			}
		} else {
			j++
		}
		i = j
	}
	return nil
}

// Save writes the active theme name to themes.toml atomically. The name
// must match a built-in theme; unknown names error without writing.
func Save(name string) error {
	if strings.TrimSpace(name) == "" {
		return fmt.Errorf("theme name cannot be empty — available: %s",
			joinNames(AvailableList()))
	}
	if _, ok := Get(name); !ok {
		return fmt.Errorf("unknown theme %q — available: %s",
			name, joinNames(AvailableList()))
	}
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
