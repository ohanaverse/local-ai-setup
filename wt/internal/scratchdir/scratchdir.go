// Package scratchdir makes and removes one throwaway directory per call: the
// only file-deleting code outside internal/removal that is not wt state, and
// it is allow-listed as such (internal/removal's fileOpAllowed: RemoveAll of
// a temp dir this package's own MkdirTemp made, never a user's files).
//
// internal/cleanup uses it for its read-only git tricks: containment and the
// diffstat run against a throwaway GIT_INDEX_FILE so the real index stays
// byte-identical, and the containment patch validates in a scratch tree.
// The classifier itself holds no deletion call (fileOpNever keeps it that
// way); all the deleting lives here, where the scan can name it.
package scratchdir

import "os"

// Temp creates a directory under the system temp root and returns it with
// the function that removes it (best effort, safe to defer and to call on
// every path). ok is false when the directory could not be made — the
// caller's operation then fails closed, leaving nothing behind.
func Temp(pattern string) (dir string, release func(), ok bool) {
	dir, err := os.MkdirTemp("", pattern)
	if err != nil {
		return "", func() {}, false
	}
	return dir, func() { _ = os.RemoveAll(dir) }, true
}
