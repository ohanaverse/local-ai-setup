package tomlw

import (
	"slices"
	"strings"
	"testing"
)

func roundTrip(t *testing.T, src string) string {
	t.Helper()
	doc, err := Decode([]byte(src))
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	out, err := Encode(doc)
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	return string(out)
}

// TestInlineRowsKeepTheirOwnKeyOrder is the golden test for the gap in the
// decoder's key list: an inline array of inline tables lists every row's keys
// with no boundary between rows. tomli-w writes `families` (and any other
// short rows) in exactly this form, so without the boundary recovery a wt
// write would reorder the keys of every row that differs from the first.
func TestInlineRowsKeepTheirOwnKeyOrder(t *testing.T) {
	const src = `families = [
    { name = "a", display_name = "A" },
    { display_name = "B", name = "b" },
    { x_rank = 2, name = "c" },
    {},
    { name = "e", x_rank = 5, display_name = "E" },
]
`
	if got := roundTrip(t, src); got != src {
		t.Errorf("the rows did not come back as written:\n%s", got)
	}
}

// TestHeaderRowsKeepTheirOwnKeyOrder pins the other array form: each
// [[models]] row keeps the key order it was written with, sub-tables
// included. A shared order would rewrite every hand-edited row. (The `tags`
// arrays keep the rows in header form: tomli-w writes rows this short inline.)
func TestHeaderRowsKeepTheirOwnKeyOrder(t *testing.T) {
	const src = `[[models]]
id = "1"
family = "x"
tags = [
    "code",
]

[models.model_info]
b = 1
a = 2

[[models]]
tags = [
    "code",
]
family = "y"
id = "2"

[models.model_info]
a = 2
c = 3
b = 1
`
	if got := roundTrip(t, src); got != src {
		t.Errorf("the rows did not come back as written:\n%s", got)
	}
}

// TestOneKeyInBothArrayFormsKeepsItsOrder pins the case the decoder's
// metadata cannot describe: one key path that is an inline array in one row
// and a [[header]] array in the next. tomli-w chooses the form per array by
// row length, so a real file can hold both.
func TestOneKeyInBothArrayFormsKeepsItsOrder(t *testing.T) {
	const src = `[[models]]
id = "short"
notes = [
    { b = 1, a = 2 },
    { a = 3, b = 4 },
]

[[models]]
id = "long"

[[models.notes]]
b = "xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx"
a = 2

[[models.notes]]
a = 3
b = 4
`
	if got := roundTrip(t, src); got != src {
		t.Errorf("the rows did not come back as written:\n%s", got)
	}
}

// TestNestedInlineRowsKeepTheirOwnKeyOrder pins the same recovery one level
// down: a time-price row's `windows` written inline, and an inline table
// inside an inline row.
func TestNestedInlineRowsKeepTheirOwnKeyOrder(t *testing.T) {
	const src = `rows = [
    { name = "a", sub = { y = 1, x = 2 }, inner = [
    { q = 1 },
    { p = 2, q = 3 },
] },
    { sub = { x = 1, y = 2 }, name = "b" },
]
`
	doc, err := Decode([]byte(src))
	if err != nil {
		t.Fatal(err)
	}
	rows, _ := doc.Get("rows")
	first := rows.([]any)[0].(*Table)
	second := rows.([]any)[1].(*Table)
	firstSub, _ := first.Get("sub")
	secondSub, _ := second.Get("sub")
	inner, _ := first.Get("inner")
	checks := []struct {
		name string
		got  []string
		want []string
	}{
		{"first row", first.Keys(), []string{"name", "sub", "inner"}},
		{"second row", second.Keys(), []string{"sub", "name"}},
		{"first row's inline table", firstSub.(*Table).Keys(), []string{"y", "x"}},
		{"second row's inline table", secondSub.(*Table).Keys(), []string{"x", "y"}},
		{"nested array, second row", inner.([]any)[1].(*Table).Keys(), []string{"p", "q"}},
	}
	for _, c := range checks {
		if !slices.Equal(c.got, c.want) {
			t.Errorf("%s: keys = %v, want %v", c.name, c.got, c.want)
		}
	}
}

// TestHandWrittenFormsGetTomliWsOrder pins the forms tomli-w never writes
// but a person does: a dotted key, a table whose parents are never declared,
// and a table continued after another one. They must come out in the order
// Python's tomllib reads them, or the first wt write after a hand edit would
// differ from the first modelman write after the same edit.
func TestHandWrittenFormsGetTomliWsOrder(t *testing.T) {
	const src = `top.dotted = 1
plain = 2

[a.b.c]
z = 1

[other]
k = "v"

[a.d]
w = 2
`
	// tomli_w.dumps(tomllib.loads(src)), run with tomli-w 1.2.0.
	const want = `plain = 2

[top]
dotted = 1

[a.b.c]
z = 1

[a.d]
w = 2

[other]
k = "v"
`
	if got := roundTrip(t, src); got != want {
		t.Errorf("got:\n%s\nwant:\n%s", got, want)
	}
}

// TestDecodeReportsAParseError pins that a file that is not TOML is an
// error: the writer must never treat an unreadable registry as an empty one
// and write over it.
func TestDecodeReportsAParseError(t *testing.T) {
	if _, err := Decode([]byte("[[models]\nid = 1\n")); err == nil {
		t.Fatal("Decode of malformed TOML should be an error")
	}
}

// TestDecodeRefusesADocumentTheDecoderLostAKeyFrom pins that Decode fails
// closed. BurntSushi v1.6.0 lists `x_note.who` here but keeps only
// `x_note.why` in the decoded tree (the path is an array in one row and a
// dotted-key table in the next). Writing that tree back would delete a key
// from the user's registry without a word, so the document is refused.
func TestDecodeRefusesADocumentTheDecoderLostAKeyFrom(t *testing.T) {
	const src = `[[models]]
id = "a"
x_note = [1]

[[models]]
id = "b"
x_note.who = "me"
x_note.why = "test"
`
	_, err := Decode([]byte(src))
	if err == nil || !strings.Contains(err.Error(), "models.x_note.who") {
		t.Fatalf("Decode = %v, want an error naming models.x_note.who", err)
	}
}

// TestDecodeRefusesAnInlineRowWithAnEmptyKey pins a known limit of the
// decoder. BurntSushi decodes an inline array holding an inline table with an
// empty key as []map[string]any (the type of a [[header]] array) and drops the
// array's other elements, so the document is refused rather than written back
// with data lost.
func TestDecodeRefusesAnInlineRowWithAnEmptyKey(t *testing.T) {
	const src = `t = [
    { n = 1 },
    { c = 1, "" = 2, a = 3 },
]
`
	_, err := Decode([]byte(src))
	if err == nil || !strings.Contains(err.Error(), "cannot place key") {
		t.Fatalf("Decode = %v, want a refusal", err)
	}
}

// TestDecodeRefusesAnEmptyKeyThatDropsElements covers the silent case: the
// decoder replaces the array with the one table and drops "keep".
func TestDecodeRefusesAnEmptyKeyThatDropsElements(t *testing.T) {
	for _, src := range []string{
		"x = [\"keep\", {\"\" = 1}]\n",
		"x = [[{\"\" = true}]]\n",
		"[a]\nx = [\"keep\", {b = {\"\" = 1}}]\n",
	} {
		_, err := Decode([]byte(src))
		if err == nil || !strings.Contains(err.Error(), "cannot place key") {
			t.Errorf("Decode(%q) = %v, want a refusal", src, err)
		}
	}
}
