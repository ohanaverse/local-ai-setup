package tomlw

import (
	"math"
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
// lands. A new key in the wrong place leaves the row laid out unlike the
// rows around it, and the same edit made on two machines could write two
// different files.
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
		{"a", "b"},
		{true, false},
		{"true", true},
	}
	for _, p := range different {
		if Same(p[0], p[1]) {
			t.Errorf("Same(%v, %v) = true, want false", p[0], p[1])
		}
	}
}

// TestTheZeroTableIsUsable pins that a Table declared without NewTable takes
// Set and SetAt like any other: the type is exported, and a nil-map panic in
// the registry writer would take the whole command down.
func TestTheZeroTableIsUsable(t *testing.T) {
	var set Table
	set.Set("a", int64(1))
	if v, ok := set.Get("a"); !ok || v != int64(1) || set.Len() != 1 {
		t.Errorf("Set on a zero Table: got %v, %v, len %d", v, ok, set.Len())
	}
	var at Table
	at.SetAt("b", "x", []string{"a", "b"})
	at.SetAt("a", "y", []string{"a", "b"})
	if got := at.Keys(); !slices.Equal(got, []string{"a", "b"}) {
		t.Errorf("SetAt on a zero Table: keys %v, want [a b]", got)
	}
	var empty Table
	if empty.Has("a") || empty.Delete("a") || empty.Clone().Len() != 0 {
		t.Error("a zero Table should read as empty")
	}
}

// TestValueRefusesANilTableInASlice pins that a nil row is refused when it is
// handed over, as a nil *Table and a nil inside []any already are: accepted,
// it would panic later in Clone or Encode, far from the caller that passed it.
func TestValueRefusesANilTableInASlice(t *testing.T) {
	if _, err := Value([]*Table{NewTable(), nil}); err == nil {
		t.Error("Value([]*Table{..., nil}) should be an error")
	}
	v, err := Value([]*Table{tableOf(t, "id", "a")})
	if err != nil || len(v.([]any)) != 1 {
		t.Errorf("a slice of real tables should convert, got %v, %v", v, err)
	}
}

// TestSameComparesTimesAsTheyAreWritten pins that two times are the same
// exactly when Encode writes them the same. Comparing instants and zone names
// got both directions wrong: a hand-written `Z` differed from the `+00:00` it
// is rewritten as, so an unchanged value looked changed, and one instant at
// two offsets compared equal, so a patch of only the offset would be skipped.
func TestSameComparesTimesAsTheyAreWritten(t *testing.T) {
	utc := time.Date(1979, 5, 27, 7, 32, 0, 0, time.UTC)
	zero := time.Date(1979, 5, 27, 7, 32, 0, 0, time.FixedZone("", 0))
	if !Same(utc, zero) {
		t.Error("Z and +00:00 are written the same and should be Same")
	}
	plusOne := time.Date(1979, 5, 27, 10, 0, 0, 0, time.FixedZone("", 3600))
	plusTwo := time.Date(1979, 5, 27, 11, 0, 0, 0, time.FixedZone("", 7200))
	if !plusOne.Equal(plusTwo) {
		t.Fatal("the two offsets should name one instant")
	}
	if Same(plusOne, plusTwo) {
		t.Error("one instant at two offsets is written differently and should not be Same")
	}
	local := time.Date(1979, 5, 27, 7, 32, 0, 0, time.FixedZone("datetime-local", 0))
	if Same(utc, local) {
		t.Error("a local datetime and an offset datetime should not be Same")
	}
}

// TestSameDoesNotPanicOnValuesOutsideTheDocument pins that two values of a
// type no document holds compare as different instead of panicking: Set
// stores what it is given, and Go's == panics on two slices or two maps.
func TestSameDoesNotPanicOnValuesOutsideTheDocument(t *testing.T) {
	pairs := [][2]any{
		{[]string{"a"}, []string{"a"}},
		{map[string]any{"a": 1}, map[string]any{"a": 1}},
		{nil, nil},
		{int64(1), nil},
	}
	for _, p := range pairs {
		if Same(p[0], p[1]) {
			t.Errorf("Same(%#v, %#v) = true, want false", p[0], p[1])
		}
	}
}

// TestSameComparesNumbersExactly pins the two edges of the number rule. An
// integer past 2^53 is not the float it rounds to, so a patch between the two
// must be written, not skipped as "unchanged"; and NaN, which Go's == calls
// different from itself, is written `nan` both times, so a document holding
// one must not look changed on every comparison.
func TestSameComparesNumbersExactly(t *testing.T) {
	const big = int64(1)<<53 + 1
	if Same(big, float64(1<<53)) || Same(float64(1<<53), big) {
		t.Error("an integer past 2^53 and the float it rounds to are different numbers")
	}
	if !Same(int64(1)<<53, float64(1<<53)) {
		t.Error("an integer and the float that holds it exactly should be Same")
	}
	if Same(int64(math.MaxInt64), math.Inf(1)) || Same(int64(math.MaxInt64), float64(1<<63)) || Same(int64(0), math.NaN()) {
		t.Error("no integer is infinity, 2^63 or NaN")
	}
	if !Same(int64(math.MinInt64), -float64(1<<63)) {
		t.Error("the smallest integer and -2^63 are one number")
	}
	if !Same(math.NaN(), math.NaN()) {
		t.Error("NaN is written the same both times and should be Same")
	}
	doc, err := Decode([]byte("v = nan\n"))
	if err != nil {
		t.Fatal(err)
	}
	if !Same(doc, doc.Clone()) {
		t.Error("a document holding nan should be Same as its copy")
	}
}

// TestANilTableDoesNotPanic pins that a nil *Table stored with Set — which
// keeps what it is given, so Value never sees it — is an error from Encode
// and plain "different" to Same, and survives Clone. Each used to be a
// nil-pointer panic that took the whole command down, far from the caller
// that passed the nil.
func TestANilTableDoesNotPanic(t *testing.T) {
	var none *Table
	docs := map[string]*Table{
		"as a sub-table":   tableOf(t, "cost", none),
		"as an inline row": tableOf(t, "rows", []any{NewTable(), none}),
		"as a header row":  tableOf(t, "rows", []any{tableOf(t, "tags", []any{"a"}), none}),
		"in an array":      tableOf(t, "mixed", []any{"a", none}),
	}
	for name, doc := range docs {
		if _, err := Encode(doc); err == nil {
			t.Errorf("Encode with a nil table %s should be an error", name)
		}
		if cp := doc.Clone(); cp.Len() != doc.Len() {
			t.Errorf("Clone with a nil table %s: %d keys, want %d", name, cp.Len(), doc.Len())
		}
	}
	if _, err := Encode(nil); err == nil {
		t.Error("Encode(nil) should be an error")
	}
	if none.Clone() != nil {
		t.Error("a nil table should copy to nil")
	}
	if Same(none, NewTable()) || Same(NewTable(), none) || Same(none, none) {
		t.Error("a nil table is not a document value and is the same as nothing")
	}
}
