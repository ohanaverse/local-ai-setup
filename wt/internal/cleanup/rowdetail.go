package cleanup

import (
	"fmt"
	"strings"
)

// This file is the row detail screen's data: what a row would take with it.
// It reads only — no ref, index or working file is written (the git calls go
// through Git, which sets GIT_OPTIONAL_LOCKS=0) — and it never fails: a
// listing that could not be read becomes one sanitised line in RowDetail.Err
// with that section empty, so the screen still opens on a row whose directory
// is gone.

// DetailListCap is how many entries of one listing the detail screen shows.
// Past it the remainder is counted, not listed: a worktree with thousands of
// untracked files must not turn the screen into a slow frame or a list with
// no end.
const DetailListCap = 20

// DetailCommit is one commit only this machine has: its abbreviated hash (8
// hex characters) and its subject, both already one clean line.
type DetailCommit struct {
	Hash    string
	Subject string
}

// DetailFile is one uncommitted file: git's status letter in the worktree
// column ("M" modified, "A" added, "D" deleted, "R" renamed, "C" copied, "U"
// unmerged, "??" untracked) and its path, already one clean line.
type DetailFile struct {
	Status string
	Path   string
}

// RowDetail is everything the detail screen says about a row beyond its
// verdict. The *More fields count the entries each list left out; they are 0
// when everything fits.
type RowDetail struct {
	Commits         []DetailCommit
	CommitsMore     int
	Uncommitted     []DetailFile
	UncommittedMore int
	Ignored         []string
	IgnoredMore     int
	// Err is "" when every listing was read, and one sanitised line saying
	// which listing failed and why otherwise; that listing's section is then
	// empty.
	Err string
	// Note is "" or one line saying why a listing is empty by construction:
	// a bare repository has no working tree and an unborn HEAD has no commits.
	// It is not a failure; Err stays "".
	Note string
}

// ReadRowDetail reads row's local-only work: the commits no remote has, and,
// for a worktree row, its uncommitted and git-ignored files. g.Dir is the
// repository directory the caller has (used for a branch row's git calls); a
// worktree row's calls run in row.Path, its own checkout. A remote row has
// nothing to list (its commits are on the remote), and a branch row has
// commits but no file lists (a branch has no working tree).
func ReadRowDetail(g Git, row Row) RowDetail {
	var d RowDetail
	if row.Kind == KindRemote {
		return d
	}
	dir, rev := g.Dir, row.Ref
	if row.Kind == KindBranch {
		if rev == "" {
			rev = "refs/heads/" + row.Branch
		}
	} else {
		if row.Path == "" {
			d.Err = "this row has no directory to read"
			return d
		}
		dir, rev = row.Path, "HEAD"
	}
	// WithoutFSMonitor: the status must not answer from (or start) the
	// fsmonitor daemon — a stale daemon's word is exactly what this listing
	// must not say — and dirtTimeout gives it the same budget every other
	// full-status read of a worktree gets (an --untracked-files=all status
	// hashes a large tree; the 30s default turns that into a failure).
	wg := WithoutFSMonitor(Git{Dir: dir, Timeout: dirtTimeout})

	if row.Bare {
		d.Note = "a bare repository: no working tree and no commits of its own to list"
		return d
	}
	unborn := false
	if row.Kind != KindBranch {
		has, why := detailHasCommit(wg)
		if why != "" {
			// A directory git cannot read (gone, not a repository) is a
			// failure, not an unborn HEAD: the screen must not say "no
			// commits yet" about it.
			d.Err = OneLine("could not read its checkout: " + why)
			return d
		}
		if unborn = !has; unborn {
			d.Note = "no commits yet"
		}
	}
	var commits []DetailCommit
	var more int
	var why string
	if !unborn {
		commits, more, why = readDetailCommits(wg, rev)
	}
	if why != "" {
		d.Err = OneLine("could not list its local-only commits: " + why)
		return d
	}
	d.Commits, d.CommitsMore = commits, more
	if row.Kind == KindBranch {
		return d
	}

	files, more, why := readDetailStatus(wg)
	if why != "" {
		d.Err = OneLine("could not list its uncommitted files: " + why)
		return d
	}
	d.Uncommitted, d.UncommittedMore = files, more

	ignored, more, why := readDetailIgnored(wg)
	if why != "" {
		d.Err = OneLine("could not list its ignored files: " + why)
		return d
	}
	d.Ignored, d.IgnoredMore = ignored, more
	return d
}

// readDetailCommits lists the commits rev holds that no remote ref has, newest
// first, capped at DetailListCap: `git log --not --remotes` is the same
// question the verdict's "unpushed" answer comes from, read again for the one
// row the user asked about.
func readDetailCommits(g Git, rev string) ([]DetailCommit, int, string) {
	args := []string{"log", "--abbrev=8", "--format=%h%x00%s", "-z", rev, "--not", "--remotes"}
	out, why := detailRun(g, args...)
	if why != "" {
		return nil, 0, why
	}
	fields := strings.Split(out, "\x00")
	var all []DetailCommit
	for i := 0; i+1 < len(fields); i += 2 {
		if fields[i] == "" {
			continue
		}
		all = append(all, DetailCommit{Hash: OneLine(fields[i]), Subject: OneLine(fields[i+1])})
	}
	cs, more := capDetail(all)
	return cs, more, ""
}

// detailHasCommit reports whether the checkout's HEAD names a commit; an
// unborn HEAD (a fresh `git init`) has none, so `git log HEAD` would fail.
// Only exit 1 (`--quiet`'s "no such commit") means unborn; any other failure
// (a missing directory exits 128) comes back as why.
func detailHasCommit(g Git) (has bool, why string) {
	o, err := g.Run("rev-parse", "--verify", "--quiet", "HEAD^{commit}")
	switch {
	case err != nil:
		return false, OneLine(err.Error())
	case o.Code == 0:
		return true, ""
	case o.Code == 1:
		return false, ""
	}
	return false, OneLine(fmt.Sprintf("git rev-parse: exit %d: %s", o.Code, OneLine(o.Stderr)))
}

// readDetailStatus lists the checkout's uncommitted entries — staged,
// unstaged, renamed, unmerged and untracked — in git's own path order.
func readDetailStatus(g Git) ([]DetailFile, int, string) {
	out, why := detailRun(g, StatusArgs()...)
	if why != "" {
		return nil, 0, why
	}
	all, err := parseDetailStatus(out)
	if err != nil {
		return nil, 0, err.Error()
	}
	fs, more := capDetail(all)
	return fs, more, ""
}

// readDetailIgnored lists the paths git ignores in the checkout, by name: a
// wholly ignored directory once, with git's trailing slash, rather than
// every file inside it.
func readDetailIgnored(g Git) ([]string, int, string) {
	out, why := detailRun(g, "ls-files", "-z", "--others", "--ignored", "--exclude-standard", "--directory")
	if why != "" {
		return nil, 0, why
	}
	var all []string
	for _, p := range strings.Split(out, "\x00") {
		if p != "" {
			all = append(all, OneLine(p))
		}
	}
	ps, more := capDetail(all)
	return ps, more, ""
}

// parseDetailStatus splits `status --porcelain=v2 -z` output into detail
// files. A rename or copy consumes its original path from the next field, the
// way git writes it; the original is not a second entry (the pair is one
// change, and the row's own listing is about what is at path).
func parseDetailStatus(raw string) ([]DetailFile, error) {
	toks := strings.Split(raw, "\x00")
	var out []DetailFile
	for i := 0; i < len(toks); i++ {
		t := toks[i]
		if t == "" {
			continue
		}
		switch t[0] {
		case '1':
			f := strings.SplitN(t, " ", 9)
			if len(f) != 9 {
				return nil, fmt.Errorf("unreadable status entry %q", flat(t))
			}
			out = append(out, DetailFile{Status: detailLetter(f[1]), Path: OneLine(f[8])})
		case '2':
			f := strings.SplitN(t, " ", 10)
			if len(f) != 10 || i+1 >= len(toks) {
				return nil, fmt.Errorf("unreadable status entry %q", flat(t))
			}
			i++
			out = append(out, DetailFile{Status: detailLetter(f[1]), Path: OneLine(f[9])})
		case 'u':
			f := strings.SplitN(t, " ", 11)
			if len(f) != 11 {
				return nil, fmt.Errorf("unreadable status entry %q", flat(t))
			}
			out = append(out, DetailFile{Status: "U", Path: OneLine(f[10])})
		case '?':
			out = append(out, DetailFile{Status: "??", Path: OneLine(strings.TrimPrefix(t, "? "))})
		case '!', '#':
			// Ignored entries (the listing above asks for them separately)
			// and the header block are not uncommitted changes.
		default:
			return nil, fmt.Errorf("unreadable status entry %q", flat(t))
		}
	}
	return out, nil
}

// detailLetter is a porcelain v2 XY pair as one letter: the worktree column
// when it says something, else the index column, else "?" for a pair that
// says nothing at all.
func detailLetter(xy string) string {
	if xy == "" {
		return "?"
	}
	for _, c := range []byte{xy[len(xy)-1], xy[0]} {
		if c != '.' {
			return string(c)
		}
	}
	return "?"
}

// capDetail keeps the first DetailListCap entries of a listing and reports
// how many were left out.
func capDetail[T any](all []T) ([]T, int) {
	if len(all) <= DetailListCap {
		return all, 0
	}
	return all[:DetailListCap], len(all) - DetailListCap
}

// detailRun runs one read-only listing and returns its stdout; a non-zero
// exit or a git that could not run becomes one sanitised line saying which
// command failed and why ("" on success).
func detailRun(g Git, args ...string) (string, string) {
	o, err := g.Run(args...)
	if err != nil {
		return "", OneLine(err.Error())
	}
	if o.Code != 0 {
		return "", OneLine(fmt.Sprintf("git %s: exit %d: %s", args[0], o.Code, OneLine(o.Stderr)))
	}
	return o.Stdout, ""
}
