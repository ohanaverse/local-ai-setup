// wt/internal/profiles/leaves.go
package profiles

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sort"
	"strings"
)

// leafRec is one value a config_content merge wrote: its path from the
// document root (one segment per object key, whatever characters it holds)
// and a hash of the value written, never the value itself. An empty Hash
// marks a legacy record, from a marker that listed top-level keys only:
// restore it unconditionally.
type leafRec struct {
	Path []string `json:"path"`
	Hash string   `json:"hash,omitempty"`
}

// collectLeaves returns the leaves mergeInto(doc, content) will write. A leaf
// is any value that is not a non-empty object; an empty object is a leaf
// only where doc has no object at that key (merging {} into an existing
// object changes nothing, so it records nothing). It reads doc as it is
// before the merge and never changes it. It descends through every object in
// content, also where doc holds a non-object: mergeInto replaces that value,
// and restore decides what to do about it.
func collectLeaves(doc, content map[string]any) []leafRec {
	return appendLeaves(nil, nil, doc, content)
}

func appendLeaves(recs []leafRec, prefix []string, doc, content map[string]any) []leafRec {
	for k, v := range content {
		path := append(append([]string(nil), prefix...), k)
		sub, isMap := v.(map[string]any)
		docSub, docIsMap := doc[k].(map[string]any)
		switch {
		case isMap && len(sub) > 0:
			recs = appendLeaves(recs, path, docSub, sub)
		case isMap && docIsMap:
			// {} merged into an existing object: a no-op.
		default:
			recs = append(recs, leafRec{Path: path, Hash: hashLeaf(v)})
		}
	}
	return recs
}

// hashLeaf is the hex SHA-256 of v's JSON form AS A READ-BACK FROM DISK:
// v is marshaled, parsed back — the same read the restore will do, which
// turns every JSON number into a float64 whatever Go type it was written
// from — and marshaled again. Hashing the round-tripped form instead of v
// itself is what makes a written leaf's hash match its restored form for
// every number: an int64 past float64's exact range (2^53 and up, such as
// a pinned reserved = 9007199254740993) marshals two different ways, and
// a hash of the write-side form alone would read that untouched leaf as
// changed by the session and leave the profile's own value un-restored.
func hashLeaf(v any) string {
	rt := v
	if b, err := json.Marshal(v); err == nil {
		var back any
		if json.Unmarshal(b, &back) == nil {
			rt = back
		}
	}
	b, err := json.Marshal(rt)
	if err != nil {
		// Never an empty hash: that would read as a legacy record.
		b = []byte("unmarshalable")
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// getPath reads the value at path; ok is false when a segment is missing or
// an intermediate value is not an object. A stored null is ok with v nil.
func getPath(doc map[string]any, path []string) (v any, ok bool) {
	cur := doc
	for i, seg := range path {
		next, found := cur[seg]
		if !found {
			return nil, false
		}
		if i == len(path)-1 {
			return next, true
		}
		cur, ok = next.(map[string]any)
		if !ok {
			return nil, false
		}
	}
	return nil, false
}

// revertLeaves puts back, in cur, the original value of each leaf recs
// names; orig is the document before the merge, nil when the file did not
// exist. A leaf is skipped when cur no longer has it (missing, or a
// container on its path is no longer an object: the session deleted or
// replaced it) and when its hash no longer matches what the merge wrote (the
// session changed it); either way the session's state wins. Otherwise the
// original value is set back, creating parent objects, or the leaf is
// deleted when orig has no value there. A legacy record (empty Hash) skips
// only the first check.
//
// Then every ancestor of a record's path that is an empty object in cur is
// settled by orig, deepest first: a non-object orig value there is set back
// (the merge replaced a scalar with the profile's subtree, and nothing the
// session wrote is left under it), no orig value deletes it, and an orig
// object leaves it empty. The root is never touched.
func revertLeaves(cur, orig map[string]any, recs []leafRec) {
	for _, r := range recs {
		v, ok := getPath(cur, r.Path)
		if !ok {
			continue
		}
		if r.Hash != "" && hashLeaf(v) != r.Hash {
			continue
		}
		if ov, has := getPath(orig, r.Path); has {
			setPath(cur, r.Path, ov)
		} else {
			deletePath(cur, r.Path)
		}
	}

	var ancestors [][]string
	seen := map[string]bool{}
	for _, r := range recs {
		for n := 1; n < len(r.Path); n++ {
			anc := r.Path[:n]
			key := strings.Join(anc, "\x00")
			if !seen[key] {
				seen[key] = true
				ancestors = append(ancestors, anc)
			}
		}
	}
	sort.SliceStable(ancestors, func(i, j int) bool { return len(ancestors[i]) > len(ancestors[j]) })
	for _, anc := range ancestors {
		v, ok := getPath(cur, anc)
		if !ok {
			continue
		}
		if m, isMap := v.(map[string]any); !isMap || len(m) > 0 {
			continue
		}
		ov, has := getPath(orig, anc)
		if _, origIsMap := ov.(map[string]any); has && origIsMap {
			continue
		}
		if has {
			setPath(cur, anc, ov)
		} else {
			deletePath(cur, anc)
		}
	}
}

// setPath sets the value at path, creating a missing parent object; a
// parent that is not an object is replaced by one.
func setPath(doc map[string]any, path []string, v any) {
	cur := doc
	for _, seg := range path[:len(path)-1] {
		next, ok := cur[seg].(map[string]any)
		if !ok {
			next = map[string]any{}
			cur[seg] = next
		}
		cur = next
	}
	cur[path[len(path)-1]] = v
}

// deletePath removes the key at path; a missing parent is a no-op.
func deletePath(doc map[string]any, path []string) {
	cur := doc
	for _, seg := range path[:len(path)-1] {
		next, ok := cur[seg].(map[string]any)
		if !ok {
			return
		}
		cur = next
	}
	delete(cur, path[len(path)-1])
}

// encodeLeafRecs is the backup marker's body.
func encodeLeafRecs(recs []leafRec) ([]byte, error) {
	return json.Marshal(recs)
}

// decodeLeafRecs reads a marker body: the current list of records, or the
// legacy list of top-level key names (records with no hash). ok is false for
// anything else, so a marker that cannot be read is never guessed at.
func decodeLeafRecs(raw []byte) (recs []leafRec, ok bool) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || raw[0] != '[' {
		return nil, false
	}
	if err := json.Unmarshal(raw, &recs); err == nil {
		for _, r := range recs {
			if len(r.Path) == 0 {
				return nil, false
			}
		}
		return recs, true
	}
	var keys []string
	if err := json.Unmarshal(raw, &keys); err != nil {
		return nil, false
	}
	recs = make([]leafRec, 0, len(keys))
	for _, k := range keys {
		recs = append(recs, leafRec{Path: []string{k}})
	}
	return recs, true
}
