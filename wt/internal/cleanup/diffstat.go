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
// Unknown. A binary (`-	-` from numstat, NUL in the first 8000 bytes for an
// untracked file, git's own rule), a file that cannot be read and one that is
// not a regular file have no countable lines, so any of them makes the whole
// stat Unknown ("-"): a confident +0/-0 would claim a change the counts
// cannot see is empty. A git command failing is Unknown too. The numstat diff runs against a throwaway copy
// of the real index: `git diff` rewrites the index's stat cache even under
// GIT_OPTIONAL_LOCKS=0, and this package never touches the real one.
func readDiffStat(wg Git) DiffStat {
	return readWorktreeDiff(wg).stat(wg)
}

// worktreeDiff is one `git diff` of a worktree against HEAD, read once for
// both consumers: the numstat block (the DIFF counts) and the reversible
// patch (the containment proof). ok is false when the diff could not be read.
// Renames are not detected (the patch must not rename), so a pure rename
// counts as its deleted and added lines rather than +0/-0.
type worktreeDiff struct {
	numstat string
	patch   string
	ok      bool
}

// readWorktreeDiff runs the one diff against a throwaway copy of the real
// index (indexCopy). With --numstat and a patch format together git writes
// the numstat block, one blank line, then the patch.
func readWorktreeDiff(wg Git) worktreeDiff {
	dg, release, ok := IndexCopy(wg)
	if !ok {
		return worktreeDiff{}
	}
	defer release()
	o, err := dg.Run("diff", "--numstat", "--binary", "--no-renames", "--no-color",
		"--no-ext-diff", "--no-textconv", "-U0", "HEAD")
	if err != nil || o.Code != 0 {
		return worktreeDiff{}
	}
	num, patch, found := strings.Cut(o.Stdout, "\n\n")
	if !found && strings.TrimSpace(num) != "" {
		return worktreeDiff{} // a numstat with no patch behind it: not the shape we asked for
	}
	return worktreeDiff{numstat: num, patch: patch, ok: true}
}

// stat turns the numstat block and wg's untracked files into the DiffStat.
func (d worktreeDiff) stat(wg Git) DiffStat {
	if !d.ok {
		return DiffStat{Unknown: true}
	}
	var out DiffStat
	entries := 0
	for _, line := range strings.Split(d.numstat, "\n") {
		if line == "" {
			continue
		}
		f := strings.SplitN(line, "\t", 3)
		if len(f) != 3 {
			return DiffStat{Unknown: true} // an unparsable line: not a number we can show
		}
		entries++
		if f[0] == "-" && f[1] == "-" {
			return DiffStat{Unknown: true} // binary: its lines cannot be counted
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
	var total int64
	for _, p := range paths {
		lines, size, counted := countUntrackedLines(filepath.Join(wg.Dir, p))
		total += int64(size)
		if total > dirtLimits.MaxBytes || !counted {
			return DiffStat{Unknown: true}
		}
		out.Added += lines
	}
	return out
}

// countUntrackedLines reports one untracked file's contribution: its newline
// count (a final unterminated line counts one) and its size in bytes, and
// whether the lines could be counted at all. A non-regular, unreadable or
// vanished file, and a binary (NUL in the first 8000 bytes, git's own rule),
// is not counted: the caller reads the whole diffstat as unknown rather than
// as a confident number that leaves the file out. Size still counts toward the
// byte cap, so a huge unreadable file cannot slip under it.
func countUntrackedLines(p string) (lines, size int, counted bool) {
	fi, err := os.Lstat(p)
	if err != nil {
		return 0, 0, false
	}
	if !fi.Mode().IsRegular() {
		return 0, int(fi.Size()), false // a symlink or special file
	}
	size = int(fi.Size())
	f, err := os.Open(p)
	if err != nil {
		return 0, size, false // unreadable
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
					return 0, size, false // binary
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
			return 0, size, false // read failed midway
		}
	}
	if sawAny && last != '\n' {
		lines++ // a final unterminated line counts one
	}
	return lines, size, true
}
