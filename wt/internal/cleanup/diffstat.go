package cleanup

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// DiffStat counts a worktree's uncommitted change diffstat-style: added and
// deleted lines of the staged+unstaged net against HEAD plus each non-ignored
// untracked file's lines. Git-ignored work never counts (it is IgnoredCount's
// subject only). Unknown is set instead of numbers whenever the counts could
// not be read (a git failure, a change-set over dirtLimits): the table shows
// "-" then, never a confident zero.
type DiffStat struct {
	Added   int
	Removed int
	Unknown bool
}

// readDiffStat reads wg's change-set counts. The tracked part is one
// `git diff --numstat HEAD`; the untracked part walks ls-files
// --exclude-standard and counts lines itself, under the same caps the dirt
// check uses. A whole change-set over DirtMaxFiles (tracked entries plus
// untracked paths) or DirtMaxBytes (untracked bytes; the tracked side's bytes
// are already bounded by dirty()'s dirtsnap check under the same limits) is
// Unknown. Binaries contribute no lines — `-	-` from numstat, NUL in the
// first 8000 bytes for an untracked file (git's own rule) — and so does a
// file that cannot be read or is not a regular file (the diffstat rule; a git
// command failing is Unknown). The numstat diff runs against a throwaway copy
// of the real index: `git diff` rewrites the index's stat cache even under
// GIT_OPTIONAL_LOCKS=0, and this package never touches the real one.
func readDiffStat(wg Git) DiffStat {
	dg, release, ok := indexCopy(wg)
	if !ok {
		return DiffStat{Unknown: true}
	}
	o, err := dg.Run("diff", "--numstat", "--no-ext-diff", "--no-textconv", "HEAD")
	release()
	if err != nil || o.Code != 0 {
		return DiffStat{Unknown: true}
	}
	var out DiffStat
	entries := 0
	for _, line := range strings.Split(o.Stdout, "\n") {
		if line == "" {
			continue
		}
		f := strings.SplitN(line, "\t", 3)
		if len(f) != 3 {
			return DiffStat{Unknown: true} // an unparsable line: not a number we can show
		}
		entries++
		if f[0] == "-" && f[1] == "-" {
			continue // binary: contributes no lines
		}
		a, errA := strconv.Atoi(f[0])
		r, errR := strconv.Atoi(f[1])
		if errA != nil || errR != nil {
			return DiffStat{Unknown: true}
		}
		out.Added += a
		out.Removed += r
	}

	ul, err := wg.Run("ls-files", "--others", "--exclude-standard", "-z")
	if err != nil || ul.Code != 0 {
		return DiffStat{Unknown: true}
	}
	var paths []string
	for _, p := range strings.Split(ul.Stdout, "\x00") {
		if p != "" {
			paths = append(paths, p)
		}
	}
	if entries+len(paths) > dirtLimits.MaxFiles {
		return DiffStat{Unknown: true}
	}
	var bytes int
	for _, p := range paths {
		lines, size := countUntrackedLines(filepath.Join(wg.Dir, p))
		bytes += size
		if int64(bytes) > dirtLimits.MaxBytes {
			return DiffStat{Unknown: true}
		}
		out.Added += lines
	}
	return out
}

// countUntrackedLines reports one untracked file's contribution: its newline
// count (a final unterminated line counts one) and its size in bytes. A
// non-regular or unreadable file contributes no lines and no bytes read
// beyond its lstat size; a binary (NUL in the first 8000 bytes, git's own
// rule) contributes no lines. Size still counts toward the byte cap, so a
// huge unreadable file cannot slip under it.
func countUntrackedLines(p string) (lines, size int) {
	fi, err := os.Lstat(p)
	if err != nil {
		return 0, 0
	}
	if !fi.Mode().IsRegular() {
		return 0, int(fi.Size()) // a symlink or special file: no lines
	}
	size = int(fi.Size())
	f, err := os.Open(p)
	if err != nil {
		return 0, size // unreadable: contributes no lines (diffstat rule)
	}
	defer f.Close()
	scanned := 0
	var last byte
	sawAny := false
	buf := make([]byte, 32*1024)
	for {
		n, err := f.Read(buf)
		if n > 0 {
			chunk := buf[:n]
			if scanned < 8000 {
				limit := min(n, 8000-scanned)
				if bytes.IndexByte(chunk[:limit], 0) >= 0 {
					return 0, size // binary
				}
				scanned += limit
			}
			lines += bytes.Count(chunk, []byte{'\n'})
			last = chunk[n-1]
			sawAny = true
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			return 0, size // read failed midway: contributes no lines
		}
	}
	if sawAny && last != '\n' {
		lines++ // a final unterminated line counts one
	}
	return lines, size
}
