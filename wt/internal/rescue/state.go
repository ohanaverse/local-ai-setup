package rescue

import (
	"crypto/sha256"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// statusArgs read a worktree's state in full: porcelain v2, every untracked
// file (not a directory summary), submodules never hidden.
var statusArgs = []string{"status", "--porcelain=v2", "-z", "--untracked-files=all", "--ignore-submodules=none"}

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

// hashPath summarises what sits at root/rel without following a symlink: its
// kind, permission bits and content (or link target). Two trees agree on a
// path exactly when these strings are equal.
func hashPath(root, rel string) string {
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
// HEAD, every ref except the new branch, the index file's bytes, the status
// listing and the content of every path the status names.
type fingerprint struct {
	head   string
	refs   string
	index  string
	status string
	files  map[string]string
}

// takeFingerprint reads sg's source. exclude is the full refname of the new
// branch, which the rescue itself adds to the shared refs.
func takeFingerprint(sg sgit, exclude string) (fingerprint, error) {
	var fp fingerprint
	var err error
	if fp.head, err = sg.text("rev-parse", "HEAD"); err != nil {
		return fp, err
	}
	refs, err := sg.out(nil, "for-each-ref", "--format=%(objectname) %(refname)")
	if err != nil {
		return fp, err
	}
	var kept []string
	for _, l := range strings.Split(string(refs), "\n") {
		if l != "" && !strings.HasSuffix(l, " "+exclude) {
			kept = append(kept, l)
		}
	}
	fp.refs = strings.Join(kept, "\n")
	idx, err := sg.text("rev-parse", "--path-format=absolute", "--git-path", "index")
	if err != nil {
		return fp, err
	}
	b, err := os.ReadFile(idx)
	switch {
	case os.IsNotExist(err):
		fp.index = "absent"
	case err != nil:
		return fp, err
	default:
		fp.index = fmt.Sprintf("%x", sha256.Sum256(b))
	}
	st, err := sg.out(nil, statusArgs...)
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
	case a.refs != b.refs:
		return "a ref"
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
