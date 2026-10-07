package tomlw

import (
	"math"
	"strings"
	"testing"
	"time"
)

func encodeOne(t *testing.T, key string, v any) string {
	t.Helper()
	doc := NewTable()
	doc.Set(key, v)
	out, err := Encode(doc)
	if err != nil {
		t.Fatalf("Encode(%v): %v", v, err)
	}
	return string(out)
}

// TestEncodeWritesValuesAsTomliWDoes pins each scalar against the text
// tomli-w 1.2.0 gives the same value (the expected strings were produced by
// tomli_w.dumps). One differing character in a float or an escape is a line
// that flips every time the other tool saves the file.
func TestEncodeWritesValuesAsTomliWDoes(t *testing.T) {
	cases := []struct {
		v    any
		want string
	}{
		{1.0, "v = 1.0\n"},
		{0.125, "v = 0.125\n"},
		{1e-07, "v = 1e-07\n"},
		{5e22, "v = 5e+22\n"},
		{1e16, "v = 1e+16\n"},
		{1e15, "v = 1000000000000000.0\n"},
		{0.0001, "v = 0.0001\n"},
		{0.00001, "v = 1e-05\n"},
		{math.Copysign(0, -1), "v = -0.0\n"},
		{123456.789, "v = 123456.789\n"},
		{math.Inf(1), "v = inf\n"},
		{math.Inf(-1), "v = -inf\n"},
		{math.NaN(), "v = nan\n"},
		{int64(9007199254740993), "v = 9007199254740993\n"},
		{int64(-1), "v = -1\n"},
		{true, "v = true\n"},
		{"plain", "v = \"plain\"\n"},
		{`q"uote`, "v = \"q\\\"uote\"\n"},
		{`back\slash`, "v = \"back\\\\slash\"\n"},
		{"tab\there", "v = \"tab\there\"\n"},
		{"nl\nx", "v = \"nl\\nx\"\n"},
		{"\b\f\r", "v = \"\\b\\f\\r\"\n"},
		{"\x1f", "v = \"\\u001f\"\n"},
		{"\x7f", "v = \"\\u007f\"\n"},
		{"ünï", "v = \"ünï\"\n"},
	}
	for _, c := range cases {
		if got := encodeOne(t, "v", c.v); got != c.want {
			t.Errorf("Encode(%#v) = %q, want %q", c.v, got, c.want)
		}
	}
}

// TestEncodeQuotesAKeyOnlyWhenItMust pins key formatting: a bare key stays
// bare, anything else is a quoted string — including a key with a dot, which
// unquoted would become a nested table.
func TestEncodeQuotesAKeyOnlyWhenItMust(t *testing.T) {
	cases := map[string]string{
		"bare-key_1": "bare-key_1 = 1\n",
		"sp ace":     "\"sp ace\" = 1\n",
		"":           "\"\" = 1\n",
		"dé":         "\"dé\" = 1\n",
		"a.b":        "\"a.b\" = 1\n",
	}
	for key, want := range cases {
		if got := encodeOne(t, key, int64(1)); got != want {
			t.Errorf("key %q: got %q, want %q", key, got, want)
		}
	}
}

// TestEncodeKeepsALocalTimesWallClock is why the stock BurntSushi encoder is
// not used: it prints a local date, datetime or time converted to UTC, so on
// a machine east of UTC `1979-05-27` is written back as `1979-05-26`. The
// values here carry the zone names BurntSushi's decoder gives the three local
// kinds, at Tokyo's offset, so the test does not depend on the machine's
// time zone.
func TestEncodeKeepsALocalTimesWallClock(t *testing.T) {
	const tokyo = 9 * 60 * 60
	cases := []struct {
		v    time.Time
		want string
	}{
		{time.Date(1979, 5, 27, 0, 0, 0, 0, time.FixedZone("date-local", tokyo)), "v = 1979-05-27\n"},
		{time.Date(1979, 5, 27, 7, 32, 0, 0, time.FixedZone("datetime-local", tokyo)), "v = 1979-05-27 07:32:00\n"},
		{time.Date(0, 1, 1, 7, 32, 0, 5000, time.FixedZone("time-local", tokyo)), "v = 07:32:00.000005\n"},
		{time.Date(2026, 10, 1, 12, 30, 0, 0, time.UTC), "v = 2026-10-01 12:30:00+00:00\n"},
		{time.Date(2026, 10, 1, 12, 30, 0, 250000000, time.FixedZone("", -(7*60+30)*60)), "v = 2026-10-01 12:30:00.250000-07:30\n"},
		// Past the sixth digit tomllib drops, it does not round.
		{time.Date(1979, 5, 27, 7, 32, 0, 123456789, time.UTC), "v = 1979-05-27 07:32:00.123456+00:00\n"},
		{time.Date(0, 1, 1, 7, 32, 0, 999, time.FixedZone("time-local", tokyo)), "v = 07:32:00\n"},
	}
	for _, c := range cases {
		if got := encodeOne(t, "v", c.v); got != c.want {
			t.Errorf("got %q, want %q", got, c.want)
		}
	}
	// And through the decoder, whatever zone this machine is in.
	const src = "ld = 1979-05-27\nldt = 1979-05-27 07:32:00\nlt = 07:32:00\nodt = 1979-05-27 07:32:00+00:00\n"
	if got := roundTrip(t, "ld = 1979-05-27\nldt = 1979-05-27T07:32:00\nlt = 07:32:00\nodt = 1979-05-27T07:32:00Z\n"); got != src {
		t.Errorf("decoded local times came back as:\n%s", got)
	}
}

// TestEncodeLaysArraysAndTablesOutAsTomliWDoes pins the layout rules: plain
// values before sub-tables, one array item per line with a trailing comma,
// empty arrays and tables in their short forms, and a header only for a
// table that has something to put under it.
func TestEncodeLaysArraysAndTablesOutAsTomliWDoes(t *testing.T) {
	doc := tableOf(t,
		"c", tableOf(t, "d", NewTable()),
		"a", []any{},
		"e", NewTable(),
		"b", []any{[]any{int64(1), int64(2)}, []any{}},
		"f", []any{NewTable()},
	)
	// tomli_w.dumps of the same document.
	const want = `a = []
b = [
    [
        1,
        2,
    ],
    [],
]
f = [
    {},
]

[c.d]

[e]
`
	out, err := Encode(doc)
	if err != nil {
		t.Fatal(err)
	}
	if string(out) != want {
		t.Errorf("got:\n%s\nwant:\n%s", out, want)
	}
	if out, err := Encode(NewTable()); err != nil || len(out) != 0 {
		t.Errorf("an empty document should encode to nothing, got %q, %v", out, err)
	}
}

// TestEncodeBreaksARowOutAtOneHundredAndOneCharacters pins the one layout
// decision that depends on length: rows that fit in 100 characters (indent
// and trailing comma included) are written inline, and one row a character
// longer turns the whole array into [[header]] tables. Off by one here and a
// family with a long display name is rewritten on every save.
func TestEncodeBreaksARowOutAtOneHundredAndOneCharacters(t *testing.T) {
	row := func(n int) []any { return []any{tableOf(t, "k", strings.Repeat("x", n))} }
	fits := encodeOne(t, "rows", row(85))
	if want := "rows = [\n    { k = \"" + strings.Repeat("x", 85) + "\" },\n]\n"; fits != want {
		t.Errorf("a 100-character row should stay inline, got:\n%s", fits)
	}
	breaks := encodeOne(t, "rows", row(86))
	if want := "[[rows]]\nk = \"" + strings.Repeat("x", 86) + "\"\n"; breaks != want {
		t.Errorf("a 101-character row should become a header table, got:\n%s", breaks)
	}
	// Characters, not bytes: 85 two-byte letters are still 100 characters.
	wide := encodeOne(t, "rows", []any{tableOf(t, "k", strings.Repeat("é", 85))})
	if !strings.HasPrefix(wide, "rows = [\n") {
		t.Errorf("the limit counts characters, not bytes; got:\n%s", wide)
	}
}

// TestEncodeRefusesAValueItCannotWrite pins that an unknown value type is an
// error naming the key, never a guess: a silently dropped or mangled key in
// registry.toml is data loss the user finds weeks later.
func TestEncodeRefusesAValueItCannotWrite(t *testing.T) {
	doc := tableOf(t, "models", []any{tableOf(t, "id", "a", "bad", uint8(1))})
	_, err := Encode(doc)
	if err == nil || !strings.Contains(err.Error(), "bad") {
		t.Fatalf("want an error naming the key, got %v", err)
	}
}

// TestEncodeNamesTheTableOfAValueItCannotWrite pins that the error for a bad
// value under a [header] table carries the whole key path, as it already does
// for an inline row: "bad: cannot encode" alone does not say which of a
// registry's many tables holds the key.
func TestEncodeNamesTheTableOfAValueItCannotWrite(t *testing.T) {
	doc := tableOf(t, "models", tableOf(t, "model_info", tableOf(t, "bad", uint8(1))))
	_, err := Encode(doc)
	if err == nil || !strings.Contains(err.Error(), "models: model_info: bad: ") {
		t.Fatalf("want an error naming models, model_info and bad, got %v", err)
	}
	long := strings.Repeat("x", 120)
	rows := tableOf(t, "models", []any{tableOf(t, "id", long, "bad", uint8(1))})
	if _, err := Encode(rows); err == nil || !strings.Contains(err.Error(), "models: bad: ") {
		t.Fatalf("want an error naming models and bad, got %v", err)
	}
}

// TestEncodeRefusesTextThatWouldNotReadBack pins the values Encode could
// print but no reader would give back: a year that is not four digits is not
// TOML at all (the registry would stop loading in wt and modelman alike), a
// UTC offset with seconds is printed without them (another instant), and a
// string or key that is not UTF-8 would be written with U+FFFD in place of
// each bad byte. Each is an error, as any other value Encode cannot write is.
func TestEncodeRefusesTextThatWouldNotReadBack(t *testing.T) {
	bad := map[string]*Table{
		"year 10000":           tableOf(t, "v", time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC)),
		"a year before 0000":   tableOf(t, "v", time.Date(-1, 1, 1, 0, 0, 0, 0, time.UTC)),
		"an offset in seconds": tableOf(t, "v", time.Date(2026, 1, 1, 0, 0, 0, 0, time.FixedZone("", 3630))),
		"a string":             tableOf(t, "v", "a\xffb"),
		"a string in a row":    tableOf(t, "rows", []any{tableOf(t, "v", "a\xffb")}),
		"a key":                tableOf(t, "a\xffb", int64(1)),
		"a key in a row":       tableOf(t, "rows", []any{tableOf(t, "a\xffb", int64(1))}),
	}
	for name, doc := range bad {
		if out, err := Encode(doc); err == nil {
			t.Errorf("%s: Encode should be an error, wrote %q", name, out)
		}
	}
	// What the decoder hands back is still written: year 0000, and a local
	// time of day, which carries the year 0 and this machine's offset.
	const src = "d = 0000-01-01\nt = 07:32:00\n"
	if got := roundTrip(t, src); got != src {
		t.Errorf("decoded values came back as %q", got)
	}
}
