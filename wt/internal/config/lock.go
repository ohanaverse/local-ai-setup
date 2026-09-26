package config

import (
	"os"
	"path/filepath"
	"syscall"
)

// WithLock serializes read-modify-write cycles on config.toml across
// processes (and goroutines within one process, since flock is scoped to
// the open file description, not the pid) with a blocking flock on
// config.toml.lock. Every writer of config.toml — Save and PatchSave —
// goes through it, so a whole-file Save and a PatchSave's locked
// read-modify-write can never interleave.
//
// Unlike internal/litellm's WithLock (config.yaml), this has no
// context-cancellable polling variant: config.toml writes are a small TOML
// encode plus an atomic rename, always fast, and no caller here has a
// deadline it needs to abandon mid-wait.
func WithLock(fn func() error) error {
	lockPath := Path() + ".lock"
	if err := os.MkdirAll(filepath.Dir(lockPath), 0o755); err != nil {
		return err
	}
	lf, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return err
	}
	defer lf.Close()
	if err := syscall.Flock(int(lf.Fd()), syscall.LOCK_EX); err != nil {
		return err
	}
	defer syscall.Flock(int(lf.Fd()), syscall.LOCK_UN) //nolint:errcheck
	return fn()
}
