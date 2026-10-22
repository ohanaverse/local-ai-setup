package config

import (
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// TestWithFileLockSerializesCallersOnOneLockFile pins the helper WithLock is
// built on, at a lock path of the caller's choosing (WithLock passes
// config.toml.lock). Two holders at once would let
// two wt processes interleave a read-modify-write and lose an edit.
func TestWithFileLockSerializesCallersOnOneLockFile(t *testing.T) {
	lock := filepath.Join(t.TempDir(), "nested", "config.toml.lock")
	var mu sync.Mutex
	active, maxActive := 0, 0
	var wg sync.WaitGroup
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			err := withFileLock(lock, func() error {
				mu.Lock()
				active++
				maxActive = max(maxActive, active)
				mu.Unlock()
				time.Sleep(10 * time.Millisecond)
				mu.Lock()
				active--
				mu.Unlock()
				return nil
			})
			if err != nil {
				t.Errorf("withFileLock: %v", err)
			}
		}()
	}
	wg.Wait()
	if maxActive != 1 {
		t.Errorf("max concurrent holders = %d, want 1", maxActive)
	}
	if _, err := os.Stat(lock); err != nil {
		t.Errorf("the lock file (and its directory) should have been created: %v", err)
	}
	sentinel := errors.New("from fn")
	if err := withFileLock(lock, func() error { return sentinel }); !errors.Is(err, sentinel) {
		t.Errorf("withFileLock should return fn's error, got %v", err)
	}
}
