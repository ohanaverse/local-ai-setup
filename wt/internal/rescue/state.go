package rescue

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"

	"github.com/ohanaverse/local-ai-setup/wt/internal/cleanup"
)

// entry is one path of `status --porcelain=v2`.
type entry struct {
	kind byte   // '1' ordinary, '2' renamed or copied, 'u' unmerged, '?' untracked
	xy   string // two status letters ('.' unchanged); "" for '?'
	sub  string // submodule field: "N..." for a plain file
	path string
	orig string // '2': the path it came from
}

// parseStatus splits `status --porcelain=v2 -z` output into entries.
func parseStatus(raw string) ([]entry, error) {
	toks := strings.Split(raw, "\x00")
	var out []entry
	for i := 0; i < len(toks); i++ {
		t := toks[i]
		if t == "" {
			continue
		}
		switch t[0] {
		case '1':
			f := strings.SplitN(t, " ", 9)
			if len(f) != 9 {
				return nil, fmt.Errorf("unreadable status entry %q", t)
			}
			out = append(out, entry{kind: '1', xy: f[1], sub: f[2], path: f[8]})
		case '2':
			f := strings.SplitN(t, " ", 10)
			if len(f) != 10 || i+1 >= len(toks) {
				return nil, fmt.Errorf("unreadable status entry %q", t)
			}
			i++
			out = append(out, entry{kind: '2', xy: f[1], sub: f[2], path: f[9], orig: toks[i]})
		case 'u':
			f := strings.SplitN(t, " ", 11)
			if len(f) != 11 {
				return nil, fmt.Errorf("unreadable status entry %q", t)
			}
			out = append(out, entry{kind: 'u', xy: f[1], sub: f[2], path: f[10]})
		case '?':
			out = append(out, entry{kind: '?', path: strings.TrimPrefix(t, "? ")})
		case '!', '#':
		default:
			return nil, fmt.Errorf("unreadable status entry %q", t)
		}
	}
	return out, nil
}

// paths are every path an entry names (a rename names two).
func paths(es []entry) []string {
	var out []string
	for _, e := range es {
		out = append(out, e.path)
		if e.orig != "" {
			out = append(out, e.orig)
		}
	}
	slices.Sort(out)
	return slices.Compact(out)
}

// leadingBlocked reports whether a proper prefix of rel (slash-separated,
// under root) is a symlink, not a directory, or missing: git's
// has_symlink_leading_path, so the path is absent from the working tree as git
// sees it. It is the same rule as dirtsnap's prefixCheck (kept unexported
// there; the fingerprint must agree with the classifier). Any other Lstat
// failure on a prefix is returned.
func leadingBlocked(root, rel string) (bool, error) {
	for i := 0; i < len(rel); i++ {
		if rel[i] != '/' {
			continue
		}
		fi, err := os.Lstat(filepath.Join(root, filepath.FromSlash(rel[:i])))
		switch {
		case errors.Is(err, os.ErrNotExist) || errors.Is(err, syscall.ENOTDIR):
			return true, nil
		case err != nil:
			return false, err
		case !fi.IsDir():
			return true, nil
		}
	}
	return false, nil
}

// hashPath summarises what sits at root/rel without following a symlink: its
// kind, permission bits and content (or link target). Two trees agree on a
// path exactly when these strings are equal. A path behind a symlink, a
// non-directory or a missing parent is "absent", as git sees it: os.Lstat
// declines to follow only the last component, so without the prefix check a
// tracked directory replaced by a symlink was read through the link. Both the
// fingerprint and verify (DestinationMatches) compare through this function.
func hashPath(root, rel string) string {
	switch blocked, err := leadingBlocked(root, rel); {
	case err != nil:
		return "error:" + err.Error()
	case blocked:
		return "absent"
	}
	p := filepath.Join(root, rel)
	fi, err := os.Lstat(p)
	switch {
	case os.IsNotExist(err):
		return "absent"
	case err != nil:
		return "error:" + err.Error()
	}
	switch m := fi.Mode(); {
	case m&os.ModeSymlink != 0:
		t, err := os.Readlink(p)
		if err != nil {
			return "error:" + err.Error()
		}
		return "link:" + t
	case m.IsRegular():
		f, err := os.Open(p)
		if err != nil {
			return "error:" + err.Error()
		}
		defer f.Close()
		h := sha256.New()
		if _, err := io.Copy(h, f); err != nil {
			return "error:" + err.Error()
		}
		return fmt.Sprintf("file:%o:%x", m.Perm(), h.Sum(nil))
	case m.IsDir():
		return "dir"
	}
	return fmt.Sprintf("other:%v", fi.Mode())
}

// fingerprint is everything about a source a rescue promises not to change:
// its HEAD, the branch ref HEAD names, the index's content (the staged
// entries, not the file's bytes, which a stat-cache refresh rewrites), the
// status listing and the content of every path the status names. Other refs,
// other worktrees and remote-tracking refs are not the source's: a parallel
// session or a background fetch changes them without touching it.
type fingerprint struct {
	head   string
	branch string // the ref HEAD names, with its commit; "" when detached
	index  string
	status string
	files  map[string]string
}

// takeFingerprint reads sg's source.
func takeFingerprint(sg sgit) (fingerprint, error) {
	var fp fingerprint
	var err error
	if fp.head, err = sg.text("rev-parse", "HEAD"); err != nil {
		return fp, err
	}
	if ref, err := sg.text("symbolic-ref", "-q", "HEAD"); err == nil && ref != "" {
		tip, err := sg.text("rev-parse", "--verify", "-q", ref)
		if err != nil {
			tip = "unborn"
		}
		fp.branch = ref + " " + tip
	}
	idx, err := sg.out(nil, "ls-files", "--stage", "-z")
	if err != nil {
		return fp, err
	}
	fp.index = fmt.Sprintf("%x", sha256.Sum256(idx))
	st, err := sg.out(nil, cleanup.StatusArgs()...)
	if err != nil {
		return fp, err
	}
	fp.status = string(st)
	es, err := parseStatus(fp.status)
	if err != nil {
		return fp, err
	}
	fp.files = map[string]string{}
	for _, p := range paths(es) {
		fp.files[p] = hashPath(sg.dir, p)
	}
	return fp, nil
}

// diff names the first thing that differs between two fingerprints ("" none).
func (a fingerprint) diff(b fingerprint) string {
	switch {
	case a.head != b.head:
		return "HEAD"
	case a.branch != b.branch:
		return "the branch"
	case a.index != b.index:
		return "the index"
	case a.status != b.status:
		return "the status"
	}
	for p, h := range a.files {
		if b.files[p] != h {
			return "file " + p
		}
	}
	if len(a.files) != len(b.files) {
		return "the file set"
	}
	return ""
}
