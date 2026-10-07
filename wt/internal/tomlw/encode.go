package tomlw

import (
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// The two layout constants of tomli-w 1.2.0 that decide where a line breaks.
const (
	arrayIndent   = "    "
	maxLineLength = 100
)

// Encode lays root out as tomli_w.dumps does (tomli-w 1.2.0, the version
// modelman pins): in each table the plain values first, in key order, then
// the sub-tables; an array of tables as one inline row per table when every
// row fits in 100 characters, else as [[header]] tables; every other array
// one item per line with a trailing comma; floats and datetimes as Python
// prints them. A value that is not a document value is an error, never a
// guess.
func Encode(root *Table) ([]byte, error) {
	var b strings.Builder
	if err := writeTable(&b, root, "", false); err != nil {
		return nil, err
	}
	return []byte(b.String()), nil
}

type subTable struct {
	key   string
	table *Table
	inAOT bool
}

// writeTable is tomli-w's gen_table_chunks.
func writeTable(b *strings.Builder, t *Table, name string, inAOT bool) error {
	var literals []string
	var tables []subTable
	for _, k := range t.keys {
		v := t.vals[k]
		if sub, ok := v.(*Table); ok {
			tables = append(tables, subTable{k, sub, false})
			continue
		}
		if rows, ok := tableRows(v); ok {
			inline, err := allRowsFitInline(rows)
			if err != nil {
				return fmt.Errorf("%s: %w", k, err)
			}
			if !inline {
				for _, row := range rows {
					tables = append(tables, subTable{k, row, true})
				}
				continue
			}
		}
		lit, err := literal(v, 0)
		if err != nil {
			return fmt.Errorf("%s: %w", k, err)
		}
		literals = append(literals, formatKey(k)+" = "+lit+"\n")
	}
	wrote := false
	if inAOT || (name != "" && (len(literals) > 0 || len(tables) == 0)) {
		wrote = true
		if inAOT {
			b.WriteString("[[" + name + "]]\n")
		} else {
			b.WriteString("[" + name + "]\n")
		}
	}
	if len(literals) > 0 {
		wrote = true
		for _, l := range literals {
			b.WriteString(l)
		}
	}
	for _, sub := range tables {
		if wrote {
			b.WriteString("\n")
		} else {
			wrote = true
		}
		display := formatKey(sub.key)
		if name != "" {
			display = name + "." + display
		}
		if err := writeTable(b, sub.table, display, sub.inAOT); err != nil {
			return fmt.Errorf("%s: %w", sub.key, err)
		}
	}
	return nil
}

// tableRows reports whether v is a non-empty array holding only tables —
// tomli-w's is_aot.
func tableRows(v any) ([]*Table, bool) {
	arr, ok := v.([]any)
	if !ok || len(arr) == 0 {
		return nil, false
	}
	rows := make([]*Table, len(arr))
	for i, e := range arr {
		t, ok := e.(*Table)
		if !ok {
			return nil, false
		}
		rows[i] = t
	}
	return rows, true
}

// allRowsFitInline is tomli-w's is_suitable_inline_table over every row: the
// row, indented and with its trailing comma, is at most 100 characters and
// holds no newline (so no non-empty array, which always spans lines).
func allRowsFitInline(rows []*Table) (bool, error) {
	for _, row := range rows {
		s, err := inlineTable(row)
		if err != nil {
			return false, err
		}
		line := arrayIndent + s + ","
		if utf8.RuneCountInString(line) > maxLineLength || strings.Contains(line, "\n") {
			return false, nil
		}
	}
	return true, nil
}

func literal(v any, nest int) (string, error) {
	switch x := v.(type) {
	case bool:
		return strconv.FormatBool(x), nil
	case int64:
		return strconv.FormatInt(x, 10), nil
	case float64:
		return formatFloat(x), nil
	case string:
		return formatString(x), nil
	case time.Time:
		return formatTime(x), nil
	case *Table:
		return inlineTable(x)
	case []any:
		return inlineArray(x, nest)
	}
	return "", fmt.Errorf("tomlw: cannot encode %T", v)
}

func inlineTable(t *Table) (string, error) {
	if len(t.keys) == 0 {
		return "{}", nil
	}
	parts := make([]string, 0, len(t.keys))
	for _, k := range t.keys {
		// tomli-w formats an inline table's values at nest level 0.
		lit, err := literal(t.vals[k], 0)
		if err != nil {
			return "", fmt.Errorf("%s: %w", k, err)
		}
		parts = append(parts, formatKey(k)+" = "+lit)
	}
	return "{ " + strings.Join(parts, ", ") + " }", nil
}

func inlineArray(arr []any, nest int) (string, error) {
	if len(arr) == 0 {
		return "[]", nil
	}
	indent := strings.Repeat(arrayIndent, nest+1)
	var b strings.Builder
	b.WriteString("[\n")
	for _, item := range arr {
		lit, err := literal(item, nest+1)
		if err != nil {
			return "", err
		}
		b.WriteString(indent + lit + ",\n")
	}
	b.WriteString(strings.Repeat(arrayIndent, nest) + "]")
	return b.String(), nil
}

// formatFloat prints f as Python's str(float) does: the shortest digits that
// read back as f, exponent form below 1e-04 and from 1e+16 up, and always a
// ".0" on a whole number so the value stays a float when read back.
func formatFloat(f float64) string {
	switch {
	case math.IsNaN(f):
		return "nan"
	case math.IsInf(f, 1):
		return "inf"
	case math.IsInf(f, -1):
		return "-inf"
	}
	sci := strconv.FormatFloat(f, 'e', -1, 64)
	exp, _ := strconv.Atoi(sci[strings.IndexByte(sci, 'e')+1:])
	if f != 0 && (exp < -4 || exp >= 16) {
		return sci
	}
	s := strconv.FormatFloat(f, 'f', -1, 64)
	if !strings.Contains(s, ".") {
		s += ".0"
	}
	return s
}

// formatTime prints t as Python's str() prints the matching datetime, date or
// time. BurntSushi marks the three local kinds with a named zone at the
// machine's offset; they are printed from their wall clock as parsed, never
// converted — converting to UTC is the stock encoder's bug. Python keeps
// microseconds, and tomllib drops any digit past the sixth when it reads, so
// the same digits are dropped here.
func formatTime(t time.Time) string {
	frac := ""
	if us := t.Nanosecond() / 1000; us != 0 {
		frac = fmt.Sprintf(".%06d", us)
	}
	switch t.Location().String() {
	case "date-local":
		return t.Format("2006-01-02")
	case "time-local":
		return t.Format("15:04:05") + frac
	case "datetime-local":
		return t.Format("2006-01-02 15:04:05") + frac
	}
	return t.Format("2006-01-02 15:04:05") + frac + t.Format("-07:00")
}

func isBareKey(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_') {
			return false
		}
	}
	return true
}

func formatKey(k string) string {
	if isBareKey(k) {
		return k
	}
	return formatString(k)
}

// formatString writes a basic string as tomli-w does: the six short escapes,
// \uXXXX for any other control character, a tab and everything else as is.
func formatString(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range s {
		switch {
		case r == '\b':
			b.WriteString(`\b`)
		case r == '\n':
			b.WriteString(`\n`)
		case r == '\f':
			b.WriteString(`\f`)
		case r == '\r':
			b.WriteString(`\r`)
		case r == '"':
			b.WriteString(`\"`)
		case r == '\\':
			b.WriteString(`\\`)
		case (r < 32 && r != '\t') || r == 127:
			fmt.Fprintf(&b, `\u%04x`, r)
		default:
			b.WriteRune(r)
		}
	}
	b.WriteByte('"')
	return b.String()
}
