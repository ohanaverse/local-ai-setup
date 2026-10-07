// Package tomlw is wt's writer for registry.toml: an ordered TOML document
// and an emitter that reproduces tomli-w's layout byte for byte.
//
// modelman writes registry.toml with tomli-w, and until modelman is retired
// both tools write the same file. Decode keeps every table's keys in document
// order, and Encode lays the document out as tomli_w.dumps does, so a wt
// write that changes nothing leaves the file byte-identical and a wt write
// that changes one row is a one-row diff.
//
// The stock BurntSushi encoder is not used: it sorts keys, which rewrites
// every line, and it formats local date and time values in UTC, which moves a
// bare date back a day on a machine east of UTC.
//
// The package imports nothing from wt and knows nothing about the registry.
package tomlw

import (
	"errors"
	"fmt"
	"math"
	"slices"
	"sort"
	"time"
)

// errNilTable is what a nil *Table gets wherever one is handed over or
// written: Set stores what it is given, so Encode meets one too.
var errNilTable = errors.New("tomlw: nil table")

// Table is a TOML table whose keys keep their order. A value is one of bool,
// int64, float64, string, time.Time, []any (an array of values) or *Table.
// Value converts other Go values into these. The zero Table is an empty table
// ready to use.
type Table struct {
	keys []string
	vals map[string]any
}

// NewTable returns an empty table.
func NewTable() *Table { return &Table{vals: map[string]any{}} }

// Len is the number of keys.
func (t *Table) Len() int { return len(t.keys) }

// Keys returns the keys in order. The slice is a copy.
func (t *Table) Keys() []string { return slices.Clone(t.keys) }

// Has reports whether k is set.
func (t *Table) Has(k string) bool {
	_, ok := t.vals[k]
	return ok
}

// Get returns the value of k.
func (t *Table) Get(k string) (any, bool) {
	v, ok := t.vals[k]
	return v, ok
}

// Set gives k the value v: in place when k is already set, else as the last
// key. v must be a document value (see Value).
func (t *Table) Set(k string, v any) {
	if _, ok := t.vals[k]; !ok {
		t.keys = append(t.keys, k)
	}
	t.put(k, v)
}

// put stores v under k, making the map on a zero Table's first write.
func (t *Table) put(k string, v any) {
	if t.vals == nil {
		t.vals = map[string]any{}
	}
	t.vals[k] = v
}

// SetAt gives k the value v: in place when k is already set, else at the
// position schema gives it. schema lists the known keys of this kind of
// table in the order they are written. A new key goes after the nearest
// earlier schema key the table has, or failing that before the nearest later
// one. A key schema does not list goes before the first schema key the table
// has, after any other unlisted keys: that is where modelman writes the keys
// it does not model.
func (t *Table) SetAt(k string, v any, schema []string) {
	if _, ok := t.vals[k]; ok {
		t.vals[k] = v
		return
	}
	t.put(k, v)
	t.keys = slices.Insert(t.keys, t.position(k, schema), k)
}

func (t *Table) position(k string, schema []string) int {
	at := slices.Index(schema, k)
	if at < 0 {
		for i, have := range t.keys {
			if slices.Contains(schema, have) {
				return i
			}
		}
		return len(t.keys)
	}
	for i := at - 1; i >= 0; i-- {
		if j := slices.Index(t.keys, schema[i]); j >= 0 {
			return j + 1
		}
	}
	for i := at + 1; i < len(schema); i++ {
		if j := slices.Index(t.keys, schema[i]); j >= 0 {
			return j
		}
	}
	return len(t.keys)
}

// Delete removes k and reports whether it was set.
func (t *Table) Delete(k string) bool {
	if _, ok := t.vals[k]; !ok {
		return false
	}
	delete(t.vals, k)
	t.keys = slices.DeleteFunc(t.keys, func(have string) bool { return have == k })
	return true
}

// Clone returns a deep copy: no table or array is shared with t. A nil table
// copies to nil.
func (t *Table) Clone() *Table {
	if t == nil {
		return nil
	}
	out := &Table{keys: slices.Clone(t.keys), vals: make(map[string]any, len(t.vals))}
	for k, v := range t.vals {
		out.vals[k] = cloneValue(v)
	}
	return out
}

func cloneValue(v any) any {
	switch x := v.(type) {
	case *Table:
		return x.Clone()
	case []any:
		out := make([]any, len(x))
		for i := range x {
			out[i] = cloneValue(x[i])
		}
		return out
	}
	return v
}

// Value converts a Go value into a document value. An int becomes int64 (no
// other integer width, and no float32, is converted), string and table
// slices become []any, and a map becomes a *Table with its
// keys sorted (a Go map has no order to keep; pass a *Table to choose one).
// Anything TOML cannot hold is an error.
func Value(v any) (any, error) {
	switch x := v.(type) {
	case bool, int64, float64, string, time.Time:
		return x, nil
	case int:
		return int64(x), nil
	case *Table:
		if x == nil {
			return nil, errNilTable
		}
		return x, nil
	case map[string]any:
		keys := make([]string, 0, len(x))
		for k := range x {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		t := NewTable()
		for _, k := range keys {
			val, err := Value(x[k])
			if err != nil {
				return nil, fmt.Errorf("%s: %w", k, err)
			}
			t.Set(k, val)
		}
		return t, nil
	case []string:
		out := make([]any, len(x))
		for i := range x {
			out[i] = x[i]
		}
		return out, nil
	case []map[string]any:
		out := make([]any, len(x))
		for i := range x {
			val, err := Value(x[i])
			if err != nil {
				return nil, err
			}
			out[i] = val
		}
		return out, nil
	case []*Table:
		out := make([]any, len(x))
		for i := range x {
			if x[i] == nil {
				return nil, errNilTable
			}
			out[i] = x[i]
		}
		return out, nil
	case []any:
		out := make([]any, len(x))
		for i := range x {
			val, err := Value(x[i])
			if err != nil {
				return nil, err
			}
			out[i] = val
		}
		return out, nil
	}
	return nil, fmt.Errorf("tomlw: %T is not a TOML value", v)
}

// Same reports whether a and b are the same document value. Tables are the
// same when they hold the same keys with the same values, in any order;
// arrays compare element by element. An integer and a float with the same
// numeric value are the same value — the rule that stops a caller's 3.0
// rewriting a file's `3` as `3.0` — and the comparison is exact, so an
// integer past 2^53 is not the float it would round to. NaN is the same as
// NaN: both are written `nan`. Two times are the same when they are written
// the same: `Z` and `+00:00` are one value, and one instant at two offsets
// is two. Anything that is not a document value, a nil table included, is
// the same as nothing, itself included.
func Same(a, b any) bool {
	switch x := a.(type) {
	case int64:
		switch y := b.(type) {
		case int64:
			return x == y
		case float64:
			return sameNumber(x, y)
		}
		return false
	case float64:
		switch y := b.(type) {
		case int64:
			return sameNumber(y, x)
		case float64:
			return x == y || (math.IsNaN(x) && math.IsNaN(y))
		}
		return false
	case time.Time:
		y, ok := b.(time.Time)
		return ok && formatTime(x) == formatTime(y)
	case *Table:
		y, ok := b.(*Table)
		if !ok || x == nil || y == nil || len(x.vals) != len(y.vals) {
			return false
		}
		for k, v := range x.vals {
			w, ok := y.vals[k]
			if !ok || !Same(v, w) {
				return false
			}
		}
		return true
	case []any:
		y, ok := b.([]any)
		if !ok || len(x) != len(y) {
			return false
		}
		for i := range x {
			if !Same(x[i], y[i]) {
				return false
			}
		}
		return true
	case bool:
		y, ok := b.(bool)
		return ok && x == y
	case string:
		y, ok := b.(string)
		return ok && x == y
	}
	return false
}

// sameNumber reports whether f is exactly the integer i. Converting i to a
// float instead would round it past 2^53 and call two different numbers one.
func sameNumber(i int64, f float64) bool {
	const limit = 1 << 63 // float64(math.MaxInt64) rounds up to this
	return f == math.Trunc(f) && f >= -limit && f < limit && int64(f) == i
}
