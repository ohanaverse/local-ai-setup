package dirtsnap

import (
	"bytes"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"
)

const treeMode = "40000"

// entry is one name in a tree. A tree entry that was loaded and edited has a
// child; an untouched one keeps its id and is never read.
type entry struct {
	mode  string
	id    string
	child *node
}

// node is a tree held in memory while edits are applied.
type node struct {
	entries map[string]*entry
}

// parseLsTree reads `ls-tree -z` output into entries, printing 040000 as 40000.
func parseLsTree(out []byte) (map[string]*entry, error) {
	entries := map[string]*entry{}
	for _, rec := range bytes.Split(out, []byte{0}) {
		if len(rec) == 0 {
			continue
		}
		meta, name, ok := bytes.Cut(rec, []byte{'\t'})
		if !ok {
			return nil, fmt.Errorf("dirtsnap: malformed ls-tree record %q", rec)
		}
		f := strings.Fields(string(meta))
		if len(f) != 3 {
			return nil, fmt.Errorf("dirtsnap: malformed ls-tree record %q", rec)
		}
		mode := strings.TrimLeft(f[0], "0")
		entries[string(name)] = &entry{mode: mode, id: f[2]}
	}
	return entries, nil
}

// encodeTree emits raw tree entries in git's order, where a tree sorts as its
// name plus a slash.
func encodeTree(entries map[string]*entry) ([]byte, error) {
	key := func(name string, e *entry) string {
		if e.mode == treeMode {
			return name + "/"
		}
		return name
	}
	names := make([]string, 0, len(entries))
	for n := range entries {
		names = append(names, n)
	}
	sort.Slice(names, func(i, j int) bool {
		return key(names[i], entries[names[i]]) < key(names[j], entries[names[j]])
	})
	var buf bytes.Buffer
	for _, n := range names {
		e := entries[n]
		raw, err := hex.DecodeString(e.id)
		if err != nil || len(raw) != 20 {
			return nil, fmt.Errorf("dirtsnap: bad object id %q for %q", e.id, n)
		}
		buf.WriteString(e.mode + " " + n)
		buf.WriteByte(0)
		buf.Write(raw)
	}
	return buf.Bytes(), nil
}

// mergeTree applies removals, then additions, to the tree held by root.
// A path is slash-separated; an addition is a blob with a mode and id.
func (b *builder) mergeTree(root *node, removals []string, adds map[string]*entry) error {
	for _, p := range removals {
		if err := b.remove(root, strings.Split(p, "/")); err != nil {
			return err
		}
	}
	paths := make([]string, 0, len(adds))
	for p := range adds {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	for _, p := range paths {
		if err := b.add(root, strings.Split(p, "/"), adds[p]); err != nil {
			return err
		}
	}
	return nil
}

// open loads a tree entry's children on first use.
func (b *builder) open(e *entry) error {
	if e.child != nil {
		return nil
	}
	out, err := b.run(nil, "ls-tree", "-z", e.id)
	if err != nil {
		return fmt.Errorf("dirtsnap: ls-tree %s: %w", e.id, err)
	}
	entries, err := parseLsTree(out)
	if err != nil {
		return err
	}
	e.child = &node{entries: entries}
	return nil
}

func (b *builder) remove(n *node, parts []string) error {
	e, ok := n.entries[parts[0]]
	if !ok {
		return nil
	}
	if len(parts) == 1 {
		delete(n.entries, parts[0])
		return nil
	}
	if e.mode != treeMode {
		return nil
	}
	if err := b.open(e); err != nil {
		return err
	}
	return b.remove(e.child, parts[1:])
}

func (b *builder) add(n *node, parts []string, leaf *entry) error {
	if len(parts) == 1 {
		n.entries[parts[0]] = leaf
		return nil
	}
	e, ok := n.entries[parts[0]]
	switch {
	case !ok || e.mode != treeMode:
		e = &entry{mode: treeMode, child: &node{entries: map[string]*entry{}}}
		n.entries[parts[0]] = e
	default:
		if err := b.open(e); err != nil {
			return err
		}
	}
	return b.add(e.child, parts[1:], leaf)
}

// write hashes the edited tree bottom-up and returns its id; an empty tree
// below the root is dropped by the caller via the empty flag.
func (b *builder) write(n *node) (id string, empty bool, err error) {
	for name, e := range n.entries {
		if e.child == nil {
			continue
		}
		cid, cempty, err := b.write(e.child)
		if err != nil {
			return "", false, err
		}
		if cempty {
			delete(n.entries, name)
			continue
		}
		e.id = cid
	}
	raw, err := encodeTree(n.entries)
	if err != nil {
		return "", false, err
	}
	args := []string{"hash-object", "-t", "tree"}
	if b.store {
		args = append(args, "-w")
	}
	out, err := b.run(raw, append(args, "--stdin")...)
	if err != nil {
		return "", false, fmt.Errorf("dirtsnap: hash-object tree: %w", err)
	}
	return strings.TrimSpace(string(out)), len(n.entries) == 0, nil
}
