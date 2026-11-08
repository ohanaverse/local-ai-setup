package dirtsnap

import (
	"bytes"
	"fmt"
	"strings"
)

// IndexTree returns the 40-hex id of the tree the index of dir records,
// built in memory from `ls-files --stage` so the index file is never
// rewritten (write-tree stores a cache-tree extension in it). write adds -w
// to every hash-object; false stores nothing. An unmerged entry gives
// ErrUnsupported. Gitlink entries are kept as entries. No temp file is
// created and nothing is deleted.
//
// It gives the answer `git write-tree` gives except for an intent-to-add
// entry (`git add -N`): ls-files prints it as a stage-0 empty blob, which
// IndexTree records and write-tree leaves out. Refusing an index holding one
// is the caller's job, as for Build's skip-worktree and assume-unchanged
// files (rescue's Preflight refuses all three).
func IndexTree(dir string, run Runner, write bool) (string, error) {
	out, err := run(nil, "ls-files", "--stage", "-z")
	if err != nil {
		return "", fmt.Errorf("dirtsnap: ls-files: %w", err)
	}
	b := &builder{run: run, store: write}
	root := &node{entries: map[string]*entry{}}
	for _, rec := range bytes.Split(out, []byte{0}) {
		if len(rec) == 0 {
			continue
		}
		meta, name, ok := bytes.Cut(rec, []byte{'\t'})
		f := strings.Fields(string(meta))
		if !ok || len(f) != 3 {
			return "", fmt.Errorf("dirtsnap: malformed ls-files record %q", rec)
		}
		if f[2] != "0" {
			return "", fmt.Errorf("%w: %s is unmerged", ErrUnsupported, name)
		}
		if err := b.add(root, strings.Split(string(name), "/"), &entry{mode: f[0], id: f[1]}); err != nil {
			return "", err
		}
	}
	id, _, err := b.write(root)
	return id, err
}
