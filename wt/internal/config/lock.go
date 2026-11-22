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
	return WithFileLock(Path()+".lock", fn)
}

// WithFileLockShared runs fn holding a blocking shared flock on lockPath:
// readers run beside one another while a writer (WithFileLock) excludes
// them. A home the process cannot write still reads — the lock file that
// cannot be created opens read-only instead (the shared lock is taken on
// what exists), and when even that fails the read proceeds unlocked, fail
// open like the pair's own posture: a torn pair needs a concurrent
// SetProxy, and a writer needs a writable home (#628).
func WithFileLockShared(lockPath string, fn func() error) error {
	locked := func(lf *os.File) error {
		if err := syscall.Flock(int(lf.Fd()), syscall.LOCK_SH); err != nil {
			return err
		}
		defer syscall.Flock(int(lf.Fd()), syscall.LOCK_UN) //nolint:errcheck
		return fn()
	}
	if err := os.MkdirAll(filepath.Dir(lockPath), 0o755); err == nil {
		if lf, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o600); err == nil {
			defer lf.Close()
			return locked(lf)
		}
	}
	if lf, err := os.Open(lockPath); err == nil {
		defer lf.Close()
		return locked(lf)
	}
	return fn()
}

// WithFileLock runs fn holding a blocking exclusive flock on lockPath,
// creating the lock file and its directory when they are missing. It is the
// one flock helper in this package: WithLock takes it for config.toml,
// UpdateRegistry — the registry write path (registry_write.go) — for
// registry.toml, and other packages' read-modify-write cycles (slotedit's
// slot-profiles.toml) for their own files. The lock file is never removed —
// removing it would let a waiter lock a file a newcomer no longer sees.
func WithFileLock(lockPath string, fn func() error) error {
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
