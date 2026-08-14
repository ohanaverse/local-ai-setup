package config

import (
	"os"
	"path/filepath"
	"testing"
)

// FileSecretStore reads secrets from files on disk. The Get method must
// return the trimmed contents — secrets files often have trailing newlines
// from editors, and an extra newline would break API key authentication.
func TestFileSecretStore_Get(t *testing.T) {
	tmp := t.TempDir()
	os.WriteFile(filepath.Join(tmp, "openrouter.key"), []byte("  sk-or-abc123\n"), 0644)

	store := &FileSecretStore{Dir: tmp}
	val, err := store.Get("openrouter.key")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if val != "sk-or-abc123" {
		t.Errorf("value = %q, want %q", val, "sk-or-abc123")
	}
}

// Missing secret files must produce an error, not an empty string. An empty
// string would be passed as an API key, producing a confusing "401 Unauthorized"
// at launch time instead of a clear "secret file not found" at config time.
func TestFileSecretStore_Get_MissingFile(t *testing.T) {
	store := &FileSecretStore{Dir: t.TempDir()}
	_, err := store.Get("nonexistent.key")
	if err == nil {
		t.Fatal("expected error for missing file")
	}
}

// Whitespace-only secret files (e.g. a key file that was accidentally
// cleared but not deleted) must return an empty string, not error. The
// caller can then decide whether an empty key is acceptable for that
// provider.
func TestFileSecretStore_Get_EmptyFile(t *testing.T) {
	tmp := t.TempDir()
	os.WriteFile(filepath.Join(tmp, "empty.key"), []byte("   \n  "), 0644)

	store := &FileSecretStore{Dir: tmp}
	val, err := store.Get("empty.key")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if val != "" {
		t.Errorf("value = %q, want empty string", val)
	}
}

// DefaultSecretsDir must mirror the XDG_CONFIG_HOME logic used by Path() so
// secrets and config live under the same directory tree. A mismatch would
// mean SecretRef values resolve to a different directory than the user expects.
func TestDefaultSecretsDir(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "/custom/xdg")
	if got := DefaultSecretsDir(); got != "/custom/xdg/agent-wt/secrets" {
		t.Errorf("DefaultSecretsDir() = %q, want %q", got, "/custom/xdg/agent-wt/secrets")
	}

	t.Setenv("XDG_CONFIG_HOME", "")
	home, _ := os.UserHomeDir()
	want := filepath.Join(home, ".config", "agent-wt", "secrets")
	if got := DefaultSecretsDir(); got != want {
		t.Errorf("DefaultSecretsDir() = %q, want %q", got, want)
	}
}
