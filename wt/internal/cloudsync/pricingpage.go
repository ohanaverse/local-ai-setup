package cloudsync

import (
	"bytes"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode"

	"golang.org/x/net/html"
)

// PricingURL is the page the ollama flow mirrors.
const PricingURL = "https://ollama.com/pricing"

// MinRows is the fewest model rows a page may hold and still be believed. A
// page with fewer is a page that parsed wrong.
const MinRows = 5

var (
	// Cells reach these patterns already folded: collectTables splits each
	// cell on runs of isSpace — a superset of Python's `\s`, so a non-breaking
	// or thin space the page restyles a cell with is gone too — and rejoins it
	// with one ASCII space. ASCII `\s` is therefore all that remains to match.
	offpeakSuffix = regexp.MustCompile(`(?i)\s*\(\s*off[\s-]*peak\s*\)\s*$`)
	// [0-9], not Python's Unicode `\d`: strconv.ParseFloat reads ASCII digits
	// only, so a price in any other digits (`$٣`) warns here and keeps the old
	// price.
	priceCell = regexp.MustCompile(`^\$\s*([0-9]+(?:\.[0-9]+)?)$`)
	// What an ollama model name looks like once the off-peak suffix is gone.
	// A cell that does not match (spaces, parentheses, a footnote mark) means
	// the page's wording changed: fail loudly instead of registering a junk
	// tag.
	modelName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:/-]*$`)
)

// ParseError says the pricing page no longer has the shape ParsePricing
// expects. Check names the check that failed.
type ParseError struct{ Check string }

func (e *ParseError) Error() string { return e.Check }

func parseErrorf(format string, args ...any) error {
	return &ParseError{Check: fmt.Sprintf(format, args...)}
}

// Unknown marks the prices of a PriceTriple whose cell was there but was not
// recognized as a price. The price is nil, and a sync keeps the registry's
// value for it instead of clearing it: only a real "-" cell clears a price.
type Unknown struct{ Input, Cache, Output bool }

// PriceTriple is one row's prices per million tokens. nil is "no price".
type PriceTriple struct {
	Input, Cache, Output *float64
	Unknown              Unknown
}

// samePrice reports whether two optional prices are the same: both absent,
// or both the same number.
func samePrice(a, b *float64) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}

// CatalogModel is one model the page lists, with its off-peak prices when
// the page has an off-peak row for it.
type CatalogModel struct {
	Name    string
	Prices  PriceTriple
	Offpeak *PriceTriple
}

// Catalog is the parsed page.
type Catalog struct {
	Models   []CatalogModel
	Warnings []string
}

// isSpace is Python's str.isspace for the characters str.split() splits on:
// Go's unicode.IsSpace plus the four ASCII separators U+001C to U+001F.
func isSpace(r rune) bool { return unicode.IsSpace(r) || (r >= 0x1c && r <= 0x1f) }

// pyRepr quotes s as Python's repr does for the text this package prints:
// single quotes, or double ones when the text holds a single quote and no
// double, with the quote it picked and any backslash escaped. It is how a
// cell is quoted in this package's warnings and errors, so one that holds a
// quote or a backslash still reads as one cell (pinned by the cases in
// pricingpage_test.go).
func pyRepr(s string) string {
	quote := byte('\'')
	if strings.Contains(s, "'") && !strings.Contains(s, `"`) {
		quote = '"'
	}
	// A byte loop, not a rune one: Python leaves printable non-ASCII as it is
	// and escapes only the quote and the backslash, which is what copying the
	// bytes does.
	var b strings.Builder
	b.WriteByte(quote)
	for i := 0; i < len(s); i++ {
		if ch := s[i]; ch == quote || ch == '\\' {
			b.WriteByte('\\')
		}
		b.WriteByte(s[i])
	}
	b.WriteByte(quote)
	return b.String()
}

// pyReprList formats a list of strings as Python's repr of a list of strings,
// which is how a message prints the header rows and the orphan rows it names
// (pinned by the cases in pricingpage_test.go).
func pyReprList(items []string) string {
	parts := make([]string, len(items))
	for i, item := range items {
		parts[i] = pyRepr(item)
	}
	return "[" + strings.Join(parts, ", ") + "]"
}

// pyReprListOfLists is pyReprList one level deeper, for a message's list of
// header rows.
func pyReprListOfLists(rows [][]string) string {
	parts := make([]string, len(rows))
	for i, row := range rows {
		parts[i] = pyReprList(row)
	}
	return "[" + strings.Join(parts, ", ") + "]"
}

// collectTables returns every <table> as a list of rows, each row a list of
// cell texts with runs of whitespace folded to one space. Its reading of a
// malformed page is fixed, quirks included (pinned by the cases in
// pricingpage_test.go): a <tr> or <td> that opens while one is open replaces
// it, and a row lands in the innermost open table.
//
// It reads tokens, not a tree. html.Parse would move and close tags the way a
// browser does, and the checks in ParsePricing are about the page as written.
//
// Three settings make the bare tokenizer read what Python's parser read. It
// treats <noscript> content as raw text unless told otherwise (html.Parse
// tells it; a tokenizer on its own must), and a pricing table inside a
// <noscript> fallback would then be no table at all. It also enters raw-text
// mode after a self-closing <script/>, <noscript/>, <textarea/>, <title/> or
// <style/>, which Python never does, and would swallow the markup that
// follows as text; it is told not to after every self-closing tag. And a
// CDATA section is skipped whole, as Python skips it, instead of being cut
// at its first ">".
// One case is still read differently and is not pinned: a <script> that
// nests a <script> inside a comment ends at another </script>.
func collectTables(page string) [][][]string {
	z := html.NewTokenizer(strings.NewReader(page))
	z.AllowCDATA(true)
	var (
		tables [][][]string
		open   []int
		row    []string
		inRow  bool
		cell   strings.Builder
		inCell bool
	)
	start := func(tag string) {
		switch {
		case tag == "table":
			tables = append(tables, nil)
			open = append(open, len(tables)-1)
		case tag == "tr" && len(open) > 0:
			row, inRow = nil, true
		case (tag == "td" || tag == "th") && inRow:
			cell.Reset()
			inCell = true
		}
	}
	end := func(tag string) {
		switch {
		case (tag == "td" || tag == "th") && inCell && inRow:
			row = append(row, strings.Join(strings.FieldsFunc(cell.String(), isSpace), " "))
			inCell = false
		case tag == "tr" && inRow && len(open) > 0:
			if len(row) > 0 {
				at := open[len(open)-1]
				tables[at] = append(tables[at], row)
			}
			row, inRow = nil, false
		case tag == "table" && len(open) > 0:
			open = open[:len(open)-1]
		}
	}
	for {
		switch z.Next() {
		case html.ErrorToken:
			return tables
		case html.StartTagToken:
			name, _ := z.TagName()
			if string(name) == "noscript" {
				z.NextIsNotRawText()
			}
			start(string(name))
		case html.EndTagToken:
			name, _ := z.TagName()
			end(string(name))
		case html.SelfClosingTagToken:
			// Before the next token is read: the tokenizer has already
			// armed raw-text mode for <script/>, <noscript/>, <textarea/>
			// and the like, as it does for their open tags.
			z.NextIsNotRawText()
			name, _ := z.TagName()
			start(string(name))
			end(string(name))
		case html.TextToken:
			if inCell && !bytes.HasPrefix(z.Raw(), []byte("<![CDATA[")) {
				cell.Write(z.Text())
			}
		}
	}
}

// columns is where each of the four columns sits in a table.
type columns struct{ model, input, cache, output int }

func (c columns) width() int { return max(c.model, c.input, c.cache, c.output) + 1 }

// mapColumns finds the four columns by their header text, so a column the
// page adds or moves does not shift the prices.
func mapColumns(header []string) (columns, bool) {
	find := func(pred func(string) bool) int {
		for i, h := range header {
			if pred(strings.ToLower(h)) {
				return i
			}
		}
		return -1
	}
	c := columns{
		model:  find(func(h string) bool { return strings.Contains(h, "model") }),
		cache:  find(func(h string) bool { return strings.Contains(h, "cache") }),
		input:  find(func(h string) bool { return strings.Contains(h, "input") && !strings.Contains(h, "cache") }),
		output: find(func(h string) bool { return strings.Contains(h, "output") }),
	}
	return c, c.model >= 0 && c.cache >= 0 && c.input >= 0 && c.output >= 0
}

// ParsePricing reads the pricing page. Every way the page can stop looking
// like a price table is a *ParseError, never a short or empty catalog: a
// catalog that lost its rows would plan the removal of every entry.
func ParsePricing(page string) (Catalog, error) {
	tables := collectTables(page)
	if len(tables) == 0 {
		return Catalog{}, parseErrorf("no <table> found on the page")
	}
	var (
		cols  columns
		rows  [][]string
		found bool
	)
	for _, table := range tables {
		if len(table) == 0 {
			continue
		}
		if c, ok := mapColumns(table[0]); ok {
			cols, rows, found = c, table[1:], true
			break
		}
	}
	if !found {
		var headers [][]string
		for _, table := range tables {
			if len(table) > 0 {
				headers = append(headers, table[0])
			}
		}
		return Catalog{}, parseErrorf("no table has Model/Input/Cached/Output headers (found headers: %s)", pyReprListOfLists(headers))
	}
	if len(rows) < MinRows {
		return Catalog{}, parseErrorf("found %d model rows, expected at least %d", len(rows), MinRows)
	}

	var warnings []string
	seen, unrecognized := 0, 0
	price := func(cell, where string) (value *float64, unknown bool) {
		seen++
		text := strings.ReplaceAll(strings.TrimFunc(cell, isSpace), ",", "")
		switch text {
		case "", "-", "—", "–":
			return nil, false
		}
		if m := priceCell.FindStringSubmatch(text); m != nil {
			if f, err := strconv.ParseFloat(m[1], 64); err == nil {
				return &f, false
			}
		}
		unrecognized++
		warnings = append(warnings, fmt.Sprintf("%s: unrecognized price %s; existing price kept", where, pyRepr(cell)))
		return nil, true
	}

	base := map[string]PriceTriple{}
	offpeak := map[string]PriceTriple{}
	var order []string
	for i, row := range rows {
		n := i + 1
		if len(row) < cols.width() {
			return Catalog{}, parseErrorf("row %d has %d cells, expected at least %d", n, len(row), cols.width())
		}
		raw := row[cols.model]
		name := strings.TrimFunc(offpeakSuffix.ReplaceAllString(raw, ""), isSpace)
		if name == "" {
			return Catalog{}, parseErrorf("row %d has an empty model name", n)
		}
		if !modelName.MatchString(name) {
			return Catalog{}, parseErrorf("row %d model name %q is not an ollama tag", n, raw)
		}
		var t PriceTriple
		t.Input, t.Unknown.Input = price(row[cols.input], name+" input")
		t.Cache, t.Unknown.Cache = price(row[cols.cache], name+" cached input")
		t.Output, t.Unknown.Output = price(row[cols.output], name+" output")
		target := base
		if name != strings.TrimFunc(raw, isSpace) {
			target = offpeak
		}
		if _, dup := target[name]; dup {
			return Catalog{}, parseErrorf("duplicate row for %q", raw)
		}
		target[name] = t
		if name == strings.TrimFunc(raw, isSpace) {
			order = append(order, name)
		}
	}

	if seen > 0 && unrecognized*2 > seen {
		return Catalog{}, parseErrorf("%d of %d price cells unrecognized — price format changed?", unrecognized, seen)
	}
	var orphans []string
	for name := range offpeak {
		if _, ok := base[name]; !ok {
			orphans = append(orphans, name)
		}
	}
	if len(orphans) > 0 {
		sort.Strings(orphans)
		return Catalog{}, parseErrorf("off-peak rows with no base row: %s", pyReprList(orphans))
	}
	models := make([]CatalogModel, 0, len(order))
	for _, name := range order {
		m := CatalogModel{Name: name, Prices: base[name]}
		if op, ok := offpeak[name]; ok {
			m.Offpeak = &op
		}
		models = append(models, m)
	}
	return Catalog{Models: models, Warnings: warnings}, nil
}
