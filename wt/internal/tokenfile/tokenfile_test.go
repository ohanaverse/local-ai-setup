package tokenfile

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestTokenFileKindsAndLooseSentence proves each failure has its own Kind and
// that only the loose-mode failure carries a complete shared sentence (the
// others leave the way out to each caller), and that a good file is trimmed.
func TestTokenFileKindsAndLooseSentence(t *testing.T) {
	dir := t.TempDir()
	write := func(name, body string, mode os.FileMode) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(body), mode); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(p, mode); err != nil {
			t.Fatal(err)
		}
		return p
	}
	kindOf := func(err error) (*Error, bool) {
		var te *Error
		return te, errors.As(err, &te)
	}

	loose := write("loose", "tok\n", 0o644)
	_, err := Load(loose)
	te, ok := kindOf(err)
	if !ok || te.Kind != KindLoose || te.Path != loose {
		t.Fatalf("loose: %v", err)
	}
	want := "token file " + loose + " is accessible by group or other (mode 0644); run 'chmod 600 " + loose + "'"
	if err.Error() != want {
		t.Errorf("loose text = %q, want %q", err.Error(), want)
	}

	for _, body := range []string{"", " \n\t\n"} {
		p := write("empty", body, 0o600)
		_, err = Load(p)
		te, ok = kindOf(err)
		if !ok || te.Kind != KindEmpty || !strings.Contains(err.Error(), p) {
			t.Errorf("empty %q: %v", body, err)
		}
	}

	missing := filepath.Join(dir, "nope")
	_, err = Load(missing)
	te, ok = kindOf(err)
	if !ok || te.Kind != KindStat || !strings.Contains(err.Error(), missing) || !strings.Contains(err.Error(), "no such file") {
		t.Errorf("missing: %v", err)
	}

	good := write("good", "  secret \n", 0o600)
	tok, err := Load(good)
	if err != nil || tok != "secret" {
		t.Errorf("good = %q, %v", tok, err)
	}
}

// TestLoadStatErrorsKeepTheMissingDistinction proves that a stat failure
// keeps its underlying OS cause inspectable: only a genuinely missing file
// reads as os.ErrNotExist (every reader maps that to "no key"), while any
// other stat failure — the ENOTDIR of a path traversing a regular file, say
// — must not. Load assigns KindStat to every os.Stat failure, so without the
// preserved cause a caller cannot tell "no key yet" from "cannot inspect the
// key"; the wt-model home's reader silently reported `(unset)` over a key
// file it never managed to stat (fix round 2, Finding 1).
func TestLoadStatErrorsKeepTheMissingDistinction(t *testing.T) {
	dir := t.TempDir()
	filler := filepath.Join(dir, "file")
	if err := os.WriteFile(filler, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}

	_, err := Load(filepath.Join(dir, "missing"))
	var te *Error
	if !errors.As(err, &te) || te.Kind != KindStat {
		t.Fatalf("missing: %v", err)
	}
	if !errors.Is(err, os.ErrNotExist) {
		t.Errorf("a missing file's stat error = %v, want errors.Is(err, os.ErrNotExist) to hold", err)
	}

	_, err = Load(filepath.Join(filler, "key")) // ENOTDIR: filler is a regular file
	if !errors.As(err, &te) || te.Kind != KindStat {
		t.Fatalf("ENOTDIR: %v", err)
	}
	if errors.Is(err, os.ErrNotExist) {
		t.Errorf("an ENOTDIR stat failure reads as missing: %v", err)
	}
	if !strings.Contains(err.Error(), "not a directory") {
		t.Errorf("ENOTDIR error text = %q, want the OS reason kept in it", err.Error())
	}
}
