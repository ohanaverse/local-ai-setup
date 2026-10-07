package tomlw

import (
	"slices"
	"testing"
	"time"
)

func tableOf(t *testing.T, kv ...any) *Table {
	t.Helper()
	out := NewTable()
	for i := 0; i < len(kv); i += 2 {
		out.Set(kv[i].(string), kv[i+1])
	}
	return out
}

// TestSetAtPutsANewKeyAtItsSchemaPosition pins where a key wt adds to a row
// lands. A new key in the wrong place is a row modelman reorders on its next
// save, so every wt edit would show up twice in the file's history.
func TestSetAtPutsANewKeyAtItsSchemaPosition(t *testing.T) {
	schema := []string{"id", "family", "location", "tags", "cost"}
	cases := []struct {
		name string
		have []string
		add  string
		want []string
	}{
		{"after the nearest earlier schema key", []string{"id", "family", "tags"}, "location", []string{"id", "family", "location", "tags"}},
		{"before the nearest later one when nothing earlier is there", []string{"tags", "cost"}, "id", []string{"id", "tags", "cost"}},
		{"last when it is the last schema key", []string{"id", "tags"}, "cost", []string{"id", "tags", "cost"}},
		{"into an empty table", nil, "family", []string{"family"}},
		{"an unlisted key goes ahead of the schema keys", []string{"id", "family"}, "catalog_name", []string{"catalog_name", "id", "family"}},
		{"and after the unlisted keys already there", []string{"x_note", "id"}, "catalog_name", []string{"x_note", "catalog_name", "id"}},
		{"an unlisted key in a table with no schema keys goes last", []string{"x_note"}, "x_more", []string{"x_note", "x_more"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			tbl := NewTable()
			for _, k := range c.have {
				tbl.Set(k, "v")
			}
			tbl.SetAt(c.add, "new", schema)
			if got := tbl.Keys(); !slices.Equal(got, c.want) {
				t.Errorf("keys = %v, want %v", got, c.want)
			}
		})
	}
}

// TestSetAtAndSetKeepAnExistingKeysPlace pins that changing a value never
// moves its line: an edit to one field must be a one-line diff.
func TestSetAtAndSetKeepAnExistingKeysPlace(t *testing.T) {
	tbl := tableOf(t, "tags", "a", "id", "b", "family", "c")
	tbl.SetAt("id", "changed", []string{"id", "family", "tags"})
	tbl.Set("tags", "changed")
	if got, want := tbl.Keys(), []string{"tags", "id", "family"}; !slices.Equal(got, want) {
		t.Errorf("keys = %v, want %v", got, want)
	}
	if v, _ := tbl.Get("id"); v != "changed" {
		t.Errorf("id = %v, want the new value", v)
	}
}

// TestDeleteRemovesTheKeyAndItsPlace pins Delete: a deleted key must not be
// emitted, and deleting a key that is not there must say so.
func TestDeleteRemovesTheKeyAndItsPlace(t *testing.T) {
	tbl := tableOf(t, "a", int64(1), "b", int64(2))
	if !tbl.Delete("a") || tbl.Delete("a") {
		t.Fatal("Delete should report true once, then false")
	}
	if got := tbl.Keys(); !slices.Equal(got, []string{"b"}) || tbl.Has("a") || tbl.Len() != 1 {
		t.Errorf("after Delete: keys %v, Has(a) %v, Len %d", got, tbl.Has("a"), tbl.Len())
	}
}

// TestCloneSharesNothing pins that a copied row is independent of its
// source: CloneModel copies a row and then overrides keys on the copy, and a
// shared nested table would change the original model too.
func TestCloneSharesNothing(t *testing.T) {
	inner := tableOf(t, "x", int64(1))
	src := tableOf(t, "cost", inner, "tags", []any{"a", tableOf(t, "k", "v")})
	cp := src.Clone()
	cpCost, _ := cp.Get("cost")
	cpCost.(*Table).Set("x", int64(2))
	cpTags, _ := cp.Get("tags")
	cpTags.([]any)[0] = "changed"
	cpTags.([]any)[1].(*Table).Set("k", "changed")

	if v, _ := inner.Get("x"); v != int64(1) {
		t.Errorf("the source's nested table changed: x = %v", v)
	}
	srcTags, _ := src.Get("tags")
	if srcTags.([]any)[0] != "a" {
		t.Errorf("the source's array changed: %v", srcTags)
	}
	if v, _ := srcTags.([]any)[1].(*Table).Get("k"); v != "v" {
		t.Errorf("a table inside the source's array changed: k = %v", v)
	}
}

// TestValueConvertsGoValues pins the conversions callers rely on when they
// pass plain Go values to the registry writer, and that a value TOML cannot
// hold is refused when it is handed over, not when the file is written.
func TestValueConvertsGoValues(t *testing.T) {
	if v, err := Value(3); err != nil || v != int64(3) {
		t.Errorf("Value(3) = %v, %v; want int64(3)", v, err)
	}
	if v, err := Value([]string{"a", "b"}); err != nil || !Same(v, []any{"a", "b"}) {
		t.Errorf("Value([]string) = %v, %v", v, err)
	}
	v, err := Value(map[string]any{"b": 1, "a": []map[string]any{{"z": true}}})
	if err != nil {
		t.Fatal(err)
	}
	tbl := v.(*Table)
	if got := tbl.Keys(); !slices.Equal(got, []string{"a", "b"}) {
		t.Errorf("a map's keys should be sorted, got %v", got)
	}
	rows, _ := tbl.Get("a")
	if _, ok := rows.([]any)[0].(*Table); !ok {
		t.Errorf("a nested map should become a table, got %T", rows.([]any)[0])
	}
	for _, bad := range []any{nil, uint8(1), float32(1), struct{}{}, []any{map[int]int{}}, (*Table)(nil)} {
		if _, err := Value(bad); err == nil {
			t.Errorf("Value(%#v) should be an error", bad)
		}
	}
}

// TestSameTreatsAnIntegerAndAFloatAlike pins the comparison behind "a price
// key is written only when its value changes": a file's `3` and a caller's
// 3.0 are the same price, and rewriting one as the other would change a line
// on every edit of that model.
func TestSameTreatsAnIntegerAndAFloatAlike(t *testing.T) {
	now := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	same := [][2]any{
		{int64(3), 3.0},
		{3.0, int64(3)},
		{"a", "a"},
		{true, true},
		{now, now},
		{[]any{int64(1), "x"}, []any{1.0, "x"}},
		{tableOf(t, "a", int64(1), "b", "x"), tableOf(t, "b", "x", "a", 1.0)},
	}
	for _, p := range same {
		if !Same(p[0], p[1]) {
			t.Errorf("Same(%v, %v) = false, want true", p[0], p[1])
		}
	}
	different := [][2]any{
		{int64(3), 3.5},
		{int64(3), "3"},
		{true, int64(1)},
		{[]any{"a", "b"}, []any{"b", "a"}},
		{[]any{"a"}, []any{"a", "b"}},
		{tableOf(t, "a", int64(1)), tableOf(t, "a", int64(1), "b", int64(2))},
		{tableOf(t, "a", int64(1)), tableOf(t, "b", int64(1))},
		{now, now.Add(time.Second)},
		{now, "2026-10-01"},
	}
	for _, p := range different {
		if Same(p[0], p[1]) {
			t.Errorf("Same(%v, %v) = true, want false", p[0], p[1])
		}
	}
}
