package tomlw

import (
	"fmt"
	"slices"
	"sort"

	"github.com/BurntSushi/toml"
)

// Decode parses a TOML document into an ordered table tree.
//
// The values come from BurntSushi/toml; the key order comes from
// MetaData.Keys(), which lists every key in document order. Three things in
// that list need care, and the order recovery below handles each:
//
//   - A [[header]] array repeats its own key once per element, so each
//     occurrence starts the next element.
//   - An inline array of inline tables lists its key once and then every
//     element's keys with no boundary between elements. The boundaries are
//     recovered from the decoded elements: a key the current element has
//     already been given, or does not have, starts the next element.
//   - A dotted key (`a.b = 1`) and an implicit super-table (`[a.b]` with no
//     `[a]`) never list the tables in between; those get their place the
//     first time a key passes through them.
//
// The result has the key order tomllib.loads gives the same text, which is
// what makes Encode(Decode(x)) equal tomli_w.dumps(tomllib.loads(x)).
//
// Decode fails closed. When the key list names a key the decoded tree has no
// place for, the two disagree about the document — BurntSushi v1.6.0 drops a
// value when a key path is an array in one [[row]] and a dotted-key table in
// a later one — and Decode returns an error instead of a tree that would
// lose the key when written back.
func Decode(data []byte) (*Table, error) {
	raw := map[string]any{}
	md, err := toml.Decode(string(data), &raw)
	if err != nil {
		return nil, err
	}
	o := &orderer{keys: md.Keys(), header: map[arrayRef]bool{}, current: map[arrayRef]int{}}
	root := o.fromRaw(raw).(*Table)
	for o.pos < len(o.keys) {
		o.step(root)
	}
	if o.lost != nil {
		return nil, fmt.Errorf("tomlw: cannot place key %s: the decoder's key list and its values disagree", o.lost)
	}
	finish(root)
	return root, nil
}

// arrayRef names one array: the table that holds it and its key there.
type arrayRef struct {
	parent *Table
	key    string
}

type orderer struct {
	keys []toml.Key
	pos  int
	// header marks the arrays written as [[header]] tables. BurntSushi
	// decodes those as []map[string]any and an inline array as []any, which
	// is the only place the two forms can be told apart per array: the
	// metadata's type is per key path, and one path can be a header array in
	// one row and an inline array in the next. One exception is known: an
	// inline array with an empty-string key in a later row also decodes as
	// []map[string]any. Its rows' keys then find no place, and Decode
	// refuses the document (lost) rather than guess their order.
	header map[arrayRef]bool
	// current is the index of the element a header array is on.
	current map[arrayRef]int
	// lost is a key the list named that the decoded tree has no place for.
	// Decode refuses such a document: the decoder dropped a value (writing
	// the tree back would delete it for good) or a row's key order cannot be
	// recovered.
	lost toml.Key
}

// fromRaw converts BurntSushi's decoded values into document values. Tables
// start with no key order; step fills it in.
func (o *orderer) fromRaw(v any) any {
	switch x := v.(type) {
	case map[string]any:
		t := &Table{vals: make(map[string]any, len(x))}
		for k, val := range x {
			if _, ok := val.([]map[string]any); ok {
				o.header[arrayRef{t, k}] = true
			}
			t.vals[k] = o.fromRaw(val)
		}
		return t
	case []map[string]any:
		out := make([]any, len(x))
		for i := range x {
			out[i] = o.fromRaw(x[i])
		}
		return out
	case []any:
		out := make([]any, len(x))
		for i := range x {
			out[i] = o.fromRaw(x[i])
		}
		return out
	}
	return v
}

// finish gives a place to any key the key list never mentioned, so every key
// is emitted: after the ordered ones, sorted.
func finish(v any) {
	switch x := v.(type) {
	case *Table:
		if len(x.keys) < len(x.vals) {
			var rest []string
			for k := range x.vals {
				if !slices.Contains(x.keys, k) {
					rest = append(rest, k)
				}
			}
			sort.Strings(rest)
			x.keys = append(x.keys, rest...)
		}
		for _, k := range x.keys {
			finish(x.vals[k])
		}
	case []any:
		for _, e := range x {
			finish(e)
		}
	}
}

// note gives k the next place in t's order, the first time it is seen.
func (t *Table) note(k string) {
	if !slices.Contains(t.keys, k) {
		t.keys = append(t.keys, k)
	}
}

// step places the key at o.pos, which is not inside an inline array.
func (o *orderer) step(root *Table) {
	k := o.keys[o.pos]
	o.pos++
	cur := root
	for _, part := range k[:len(k)-1] {
		if !cur.Has(part) {
			o.lost = k
			return
		}
		cur.note(part)
		switch v := cur.vals[part].(type) {
		case *Table:
			cur = v
		case []any:
			ref := arrayRef{cur, part}
			n, started := o.current[ref]
			if !o.header[ref] || !started || n >= len(v) {
				return
			}
			cur = v[n].(*Table)
		default:
			return
		}
	}
	last := k[len(k)-1]
	if !cur.Has(last) {
		o.lost = k
		return
	}
	cur.note(last)
	ref := arrayRef{cur, last}
	if o.header[ref] {
		if n, started := o.current[ref]; started {
			o.current[ref] = n + 1
		} else {
			o.current[ref] = 0
		}
		return
	}
	if arr, ok := cur.vals[last].([]any); ok {
		o.inlineArray(arr, k)
	}
}

// inlineArray hands the keys that follow an inline array's own key to its
// elements, in order, then drops any key under prefix that no element took.
func (o *orderer) inlineArray(arr []any, prefix toml.Key) {
	o.elements(arr, prefix)
	for o.pos < len(o.keys) && under(o.keys[o.pos], prefix) {
		o.pos++
	}
}

func (o *orderer) elements(arr []any, prefix toml.Key) {
	for _, e := range arr {
		switch x := e.(type) {
		case *Table:
			o.inlineTable(x, prefix)
		case []any:
			o.elements(x, prefix)
		}
	}
}

// inlineTable takes keys for one element until the next key is not this
// element's: it is outside prefix, the element does not have it, or the
// element was already given it.
func (o *orderer) inlineTable(t *Table, prefix toml.Key) {
	for o.pos < len(o.keys) && under(o.keys[o.pos], prefix) {
		k := o.keys[o.pos]
		rel := k[len(prefix):]
		last := rel[len(rel)-1]
		owner, ok := descend(t, rel[:len(rel)-1])
		if !ok || !owner.Has(last) || slices.Contains(owner.keys, last) {
			return
		}
		cur := t
		for _, part := range rel[:len(rel)-1] {
			cur.note(part)
			cur = cur.vals[part].(*Table)
		}
		owner.note(last)
		o.pos++
		if arr, ok := owner.vals[last].([]any); ok {
			o.inlineArray(arr, k)
		}
	}
}

// descend follows path through nested tables of t.
func descend(t *Table, path []string) (*Table, bool) {
	for _, part := range path {
		sub, ok := t.vals[part].(*Table)
		if !ok {
			return nil, false
		}
		t = sub
	}
	return t, true
}

// under reports whether k is a key strictly inside prefix.
func under(k, prefix toml.Key) bool {
	return len(k) > len(prefix) && slices.Equal(k[:len(prefix)], prefix)
}
