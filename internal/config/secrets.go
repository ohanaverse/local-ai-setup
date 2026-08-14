package config

import (
	"os"
	"path/filepath"
	"strings"
)

// SecretStore resolves opaque secret references to values.
type SecretStore interface {
	Get(ref string) (string, error)
}

// FileSecretStore reads secrets from files in a directory.
type FileSecretStore struct {
	Dir string
}

// Get reads the file named by ref and returns its trimmed contents.
func (s *FileSecretStore) Get(ref string) (string, error) {
	data, err := os.ReadFile(filepath.Join(s.Dir, ref))
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(data)), nil
}

// DefaultSecretsDir returns the default secrets directory.
func DefaultSecretsDir() string {
	return filepath.Join(configDir(), "secrets")
}

// configDir is the base config directory (shared with Path).
func configDir() string {
	base := os.Getenv("XDG_CONFIG_HOME")
	if base == "" {
		home, _ := os.UserHomeDir()
		base = filepath.Join(home, ".config")
	}
	return filepath.Join(base, "agent-wt")
}
