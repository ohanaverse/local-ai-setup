package sessionclaim

import (
	"os"
	"path/filepath"
)

// Prune deletes the stale claims in commonDir/wt-sessions and returns how many
// it removed. It reuses scan's prune rule rather than restating it: a claim is
// removed when it is malformed, or its pid is dead, or the pid runs but started
// at a different time (the OS reused it for an unrelated process, off by more
// than startTolerance). A live claim and a running pid whose start time cannot
// be read are kept — the latter is scan's "neither returned nor deleted", a
// claim nobody can verify is nobody's to drop. Removals go through the one
// prune inside scan, so this adds no second deletion path.
//
// Unlike the lock-free readers, Prune takes wt-sessions' exclusive flock
// (withLock) because it deletes files: it must not interleave with a concurrent
// Hold or Join that writes and prunes under the same lock. The zero Claim scan
// is handed never matches a claim's non-empty Path, so Prune ignores the live
// set it returns and only counts the files scan removed. A commonDir with no
// wt-sessions directory is nothing to prune and no error, and — like the
// readers — it creates nothing: the missing case is decided before the lock,
// since withLock would MkdirAll the directory and create its .lock file.
func Prune(commonDir string) (int, error) {
	dir := filepath.Join(commonDir, "wt-sessions")
	if _, err := os.Stat(dir); os.IsNotExist(err) {
		return 0, nil
	}
	removed := 0
	err := withLock(dir, func() error {
		before, err := readClaims(dir)
		if err != nil {
			return err
		}
		if _, err := scan(dir, Claim{}, nil, true); err != nil {
			return err
		}
		after, err := readClaims(dir)
		if err != nil {
			return err
		}
		removed = len(before) - len(after)
		return nil
	})
	return removed, err
}
