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
	return withFileLock(Path()+".lock", fn)
}

// withFileLock runs fn holding a blocking exclusive flock on lockPath,
// creating the lock file and its directory when they are missing. It is the
// one flock helper in this package: WithLock uses it for config.toml, and the
// registry writer for registry.toml. The lock file is never removed —
// removing it would let a waiter lock a file a newcomer no longer sees.
func withFileLock(lockPath string, fn func() error) error {
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
