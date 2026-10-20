// wt/internal/profiles/leaves.go
package profiles

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
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

// hashLeaf is the hex SHA-256 of v's JSON form. JSON is the form the
// settings file holds, so a TOML int64 and the float64 read back from disk
// hash alike.
func hashLeaf(v any) string {
	b, err := json.Marshal(v)
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
