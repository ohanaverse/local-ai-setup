package modelserver

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestLoadTokenRefusesGroupReadable pins that a token file with any group or
// other permission bit is refused with an error naming the path and the
// chmod 600 fix, while a 0600 file loads with its whitespace trimmed. The
// token is the only thing between the network and model start/stop.
func TestLoadTokenRefusesGroupReadable(t *testing.T) {
	p := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(p, []byte("  s3cret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(p, 0o640); err != nil {
		t.Fatal(err)
	}
	_, err := LoadToken(p)
	if err == nil || !strings.Contains(err.Error(), "chmod 600") || !strings.Contains(err.Error(), p) {
		t.Fatalf("0640 err = %v, want one naming the path and chmod 600", err)
	}
	if err := os.Chmod(p, 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := LoadToken(p)
	if err != nil || got != "s3cret" {
		t.Fatalf("0600 = %q, %v; want s3cret", got, err)
	}
}

// TestLoadTokenRejectsEmptyAndMissing pins that an empty or missing token
// file is an error rather than a silently open server.
func TestLoadTokenRejectsEmptyAndMissing(t *testing.T) {
	dir := t.TempDir()
	empty := filepath.Join(dir, "empty")
	if err := os.WriteFile(empty, []byte(" \n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadToken(empty); err == nil {
		t.Error("empty file: want an error")
	}
	if _, err := LoadToken(filepath.Join(dir, "none")); err == nil {
		t.Error("missing file: want an error")
	}
}
