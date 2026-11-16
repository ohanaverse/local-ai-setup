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
