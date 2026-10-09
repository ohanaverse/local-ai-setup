package cloudsync

import (
	"errors"
	"fmt"
	"os"
	"reflect"
	"strings"
	"testing"
)

const pageHead = "<tr><th>Model</th><th>Input</th><th>Cached input</th><th>Output</th></tr>"

func pageRow(name, in, cache, out string) string {
	return fmt.Sprintf(`<tr><td><a href="/library/%s">%s</a></td><td>%s</td><td>%s</td><td>%s</td></tr>`, name, name, in, cache, out)
}

// page is a pricing page with five plain rows (m0 to m4) and the given rows
// after them, so a test adds the one row it is about and still clears MinRows.
func page(head string, rows ...string) string {
	var b strings.Builder
	b.WriteString("<html><table><thead>" + head + "</thead><tbody>")
	for i := range 5 {
		b.WriteString(pageRow(fmt.Sprintf("m%d", i), "$1.00", "$0.10", "$2.00"))
	}
	b.WriteString(strings.Join(rows, "") + "</tbody></table></html>")
	return b.String()
}

func parsed(t *testing.T, html string) map[string]CatalogModel {
	t.Helper()
	catalog, err := ParsePricing(html)
	if err != nil {
		t.Fatalf("ParsePricing: %v", err)
	}
	byName := map[string]CatalogModel{}
	for _, m := range catalog.Models {
		byName[m.Name] = m
	}
	return byName
}

func wantTriple(t *testing.T, what string, got PriceTriple, in, cache, out *float64) {
	t.Helper()
	if !samePrice(got.Input, in) || !samePrice(got.Cache, cache) || !samePrice(got.Output, out) {
		t.Errorf("%s = %s/%s/%s, want %s/%s/%s", what, num(got.Input), num(got.Cache), num(got.Output), num(in), num(cache), num(out))
	}
}

// TestParsePricingFixture reads a saved copy of ollama.com/pricing, the page
// as it was no later than 2026-10-01, and pins what wt takes from it: the
// model count, base and off-peak prices, a "-" cell as no price, and no
// off-peak row passing as a model. If this fails after the page was replaced
// by a newer copy, the expected values are what to update; if it fails with
// the same file, the parser changed what a sync writes to the registry.
func TestParsePricingFixture(t *testing.T) {
	data, err := os.ReadFile("testdata/ollama_pricing.html")
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := ParsePricing(string(data))
	if err != nil {
		t.Fatalf("ParsePricing: %v", err)
	}
	if len(catalog.Models) != 17 || len(catalog.Warnings) != 0 {
		t.Fatalf("models = %d, warnings = %q; want 17 models and no warnings", len(catalog.Models), catalog.Warnings)
	}
	byName := map[string]CatalogModel{}
	for _, m := range catalog.Models {
		byName[m.Name] = m
		if strings.Contains(strings.ToLower(m.Name), "off-peak") {
			t.Errorf("an off-peak row became a model: %q", m.Name)
		}
	}
	pro := byName["deepseek-v4-pro"]
	wantTriple(t, "deepseek-v4-pro", pro.Prices, f(1.32), f(0.044), f(3.96))
	if pro.Offpeak == nil {
		t.Fatal("deepseek-v4-pro has no off-peak prices")
	}
	wantTriple(t, "deepseek-v4-pro off-peak", *pro.Offpeak, f(0.66), f(0.022), f(1.98))
	if flash := byName["deepseek-v4.1-flash"]; flash.Offpeak == nil {
		t.Error("deepseek-v4.1-flash has no off-peak prices")
	} else {
		wantTriple(t, "deepseek-v4.1-flash off-peak", *flash.Offpeak, f(0.15), f(0.003), f(0.60))
	}
	if byName["gemma4"].Offpeak != nil {
		t.Error("gemma4 has off-peak prices; the page lists none")
	}
	if got := byName["gpt-oss:120b"].Prices.Input; !samePrice(got, f(0.15)) {
		t.Errorf("gpt-oss:120b input = %s, want 0.15", num(got))
	}
	if got := byName["mistral-large-3"].Prices; got.Cache != nil || got.Unknown.Cache {
		t.Errorf("mistral-large-3 cache = %s (unknown %v), want no price from its \"-\" cell", num(got.Cache), got.Unknown.Cache)
	}
}

// TestParsePricingFindsColumnsByHeader pins that prices are read by header
// text, not position. When ollama adds or reorders a column, a positional
// parser would write the output price into the input field of every entry
// without failing.
func TestParsePricingFindsColumnsByHeader(t *testing.T) {
	head := "<tr><th>Output</th><th>Model</th><th>Cached Input</th><th>Input</th></tr>"
	var rows strings.Builder
	for i := range 5 {
		fmt.Fprintf(&rows, "<tr><td>$2</td><td>m%d</td><td>$0.1</td><td>$1</td></tr>", i)
	}
	rows.WriteString(`<tr><td>$9.00</td><td><a href="#">zz</a></td><td>$0.50</td><td>$3.00</td></tr>`)
	got := parsed(t, "<table><thead>"+head+"</thead><tbody>"+rows.String()+"</tbody></table>")
	wantTriple(t, "zz", got["zz"].Prices, f(3), f(0.5), f(9))
}

// TestParsePricingFoldsOffpeakRowsIntoTheirBase pins that a "(off-peak)" row
// becomes its model's off-peak prices whatever its capitalisation and
// wherever it stands, here before its base row. Reading it as a second model
// would register a junk entry and try to pull a tag that does not exist.
func TestParsePricingFoldsOffpeakRowsIntoTheirBase(t *testing.T) {
	got := parsed(t, page(pageHead,
		pageRow("zz (off-peak)", "$0.50", "-", "$1.00"),
		pageRow("zz", "$1.00", "$0.10", "$2.00"),
		pageRow("yy", "$1,000.50", "$0.10", "$2.00"),
		pageRow("yy (Off-Peak)", "$ 0.25", "—", "$1.00"),
	))
	if len(got) != 7 {
		t.Fatalf("models = %d, want the 5 plain rows plus zz and yy", len(got))
	}
	if got["zz"].Offpeak == nil || got["yy"].Offpeak == nil {
		t.Fatalf("off-peak rows were not folded in: zz %v, yy %v", got["zz"].Offpeak, got["yy"].Offpeak)
	}
	wantTriple(t, "zz off-peak", *got["zz"].Offpeak, f(0.5), nil, f(1))
	wantTriple(t, "yy", got["yy"].Prices, f(1000.5), f(0.1), f(2))
	wantTriple(t, "yy off-peak", *got["yy"].Offpeak, f(0.25), nil, f(1))
}

// TestParsePricingFailsLoudly pins every check that turns a changed page
// into a *ParseError naming what failed, instead of a short catalog. This is
// the first half of the safety net: a catalog that silently lost its rows
// would plan the removal of every ollama cloud entry in the registry.
func TestParsePricingFailsLoudly(t *testing.T) {
	sixBad := ""
	for i := range 6 {
		sixBad += pageRow(fmt.Sprintf("m%d", i), "USD 1", "USD 1", "USD 1")
	}
	cases := []struct{ name, html, want string }{
		{"no table", "<html><div>pricing moved</div></html>", "no <table>"},
		{"a header renamed", page("<tr><th>Model</th><th>Prompt</th><th>Cached input</th><th>Output</th></tr>"), `['Model', 'Prompt', 'Cached input', 'Output']`},
		{"too few rows", "<table>" + pageHead + pageRow("a", "$1", "$1", "$1") + "</table>", "expected at least 5"},
		{"off-peak row with no base row", page(pageHead, pageRow("ghost (Off-Peak)", "$1", "$1", "$1")), "no base row"},
		{"the same model twice", page(pageHead, pageRow("m0", "$1", "$1", "$1")), "duplicate"},
		{"most prices unreadable", "<table>" + pageHead + sixBad + "</table>", "unrecognized"},
		{"a row with too few cells", page(pageHead, "<tr><td>short</td><td>$1</td></tr>"), "row 6 has 2 cells"},
		{"an empty model name", page(pageHead, pageRow("", "$1", "$1", "$1")), "empty model name"},
		{"new off-peak wording", page(pageHead, pageRow("glm-5.3 (Off-peak hours)", "$1.00", "$0.10", "$2.00")), "not an ollama tag"},
		{"off-peak without parentheses", page(pageHead, pageRow("glm-5.3 off-peak", "$1.00", "$0.10", "$2.00")), "not an ollama tag"},
		{"a footnote mark", page(pageHead, pageRow("glm-5.3*", "$1.00", "$0.10", "$2.00")), "not an ollama tag"},
		{"a display name", page(pageHead, pageRow("GLM 5.3", "$1.00", "$0.10", "$2.00")), "not an ollama tag"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			catalog, err := ParsePricing(tc.html)
			var parseErr *ParseError
			if !errors.As(err, &parseErr) {
				t.Fatalf("err = %v (catalog of %d models), want a *ParseError", err, len(catalog.Models))
			}
			if !strings.Contains(parseErr.Check, tc.want) {
				t.Errorf("check = %q, want it to name %q", parseErr.Check, tc.want)
			}
		})
	}
}

// TestParsePricingUnknownCellWarns pins the middle ground: one price cell in
// a new format is a warning and an Unknown mark, not a failed page and not a
// cleared price. The planner keeps the registry's value for a field marked
// Unknown, so a single odd cell cannot erase a price.
func TestParsePricingUnknownCellWarns(t *testing.T) {
	catalog, err := ParsePricing(page(pageHead, pageRow("zz", "Free", "$0.10", "$2.00")))
	if err != nil {
		t.Fatalf("ParsePricing: %v", err)
	}
	zz := catalog.Models[len(catalog.Models)-1]
	if zz.Name != "zz" || zz.Prices.Input != nil || zz.Prices.Unknown != (Unknown{Input: true}) {
		t.Errorf("zz = %+v, want no input price, marked unknown", zz)
	}
	// Single quotes: modelman prints the cell with Python's repr (`{cell!r}`),
	// and the two tools' plans are meant to read the same.
	if want := []string{`zz input: unrecognized price 'Free'; existing price kept`}; !reflect.DeepEqual(catalog.Warnings, want) {
		t.Errorf("warnings = %q, want %q", catalog.Warnings, want)
	}
}

// TestPyReprMatchesPythonsRepr pins the two branches of the quoting a plan's
// warnings carry: a cell whose text holds an apostrophe is the one case
// Python's repr switches to double quotes, and a plan that quoted it the
// other way would read differently from modelman's for the same page.
func TestPyReprMatchesPythonsRepr(t *testing.T) {
	for in, want := range map[string]string{
		"Free":            "'Free'",
		"~$3/mo":          "'~$3/mo'",
		`don't know`:      `"don't know"`,
		`a ' and a " one`: `'a \' and a " one'`,
	} {
		if got := pyRepr(in); got != want {
			t.Errorf("pyRepr(%q) = %s, want %s", in, got, want)
		}
	}
	if got := pyReprList([]string{"a", "b"}); got != "['a', 'b']" {
		t.Errorf("pyReprList = %s, want ['a', 'b']", got)
	}
}

// TestCollectTablesReadsMarkupAsModelmanDid pins the tokenizer against the
// outputs of modelman's HTMLParser-based collector on the same inputs (the
// expected values were produced by running that collector). The two tools
// share one fixture and, until modelman is deleted, one registry: a page one
// of them reads differently is a page they would sync differently.
func TestCollectTablesReadsMarkupAsModelmanDid(t *testing.T) {
	cases := []struct {
		name, html string
		want       [][][]string
	}{
		{"script text is cell text, its tags are not", "<table><tr><td>a<script>var x = '<td>no</td>';</script>b</td></tr></table>", [][][]string{{{"avar x = '<td>no</td>';b"}}}},
		{"a comment is skipped", "<table><tr><td>a<!-- <td>no</td> -->b</td></tr></table>", [][][]string{{{"ab"}}}},
		{"> inside a quoted attribute", `<table><tr><td title="x > y">a</td><td>b</td></tr></table>`, [][][]string{{{"a", "b"}}}},
		{"entities are decoded", "<table><tr><td>&amp;&nbsp;&lt;x&gt; &#36;1</td></tr></table>", [][][]string{{{"& <x> $1"}}}},
		{"a cell that reopens replaces the open one", "<table><tr><td>a<td>b</td></tr></table>", [][][]string{{{"b"}}}},
		{"a nested table takes the row", "<table><tr><td>outer<table><tr><td>inner</td></tr></table>tail</td></tr></table>", [][][]string{nil, {{"inner"}}}},
		{"uppercase tags", "<TABLE><TR><TH>Model</TH></TR><TR><TD>A</TD></TR></TABLE>", [][][]string{{{"Model"}, {"A"}}}},
		{"a self-closing cell is an empty cell", "<table><tr><td/><td>x</td></tr></table>", [][][]string{{{"", "x"}}}},
		{"a row outside any table is dropped", "<tr><td>stray</td></tr><table><tr><td>in</td></tr></table>", [][][]string{{{"in"}}}},
		{"whitespace is folded, no-break space included", "<table><tr><td>  a \n\t b\u00a0c  </td></tr></table>", [][][]string{{{"a b c"}}}},
		{"a row with no cell is dropped", "<table><tr></tr><tr><td></td></tr></table>", [][][]string{{{""}}}},
		{"a row that reopens replaces the open one", "<table><tr><td>lost</td><tr><td>kept</td></tr></table>", [][][]string{{{"kept"}}}},
		{"a table inside <noscript> is read", "<noscript><table><tr><td>a</td></tr></table></noscript>", [][][]string{{{"a"}}}},
		{"markup inside <noscript> is markup", "<table><tr><td>a<noscript>n<td>x</td></noscript>b</td></tr></table>", [][][]string{{{"x"}}}},
		// A self-closing tag never opens raw text in Python's parser, so the
		// markup after <noscript/> or <script/> is still markup.
		{"a self-closing <noscript/> swallows nothing", "<table><tr><td>a<noscript/>b<td>c</td></tr></table>", [][][]string{{{"c"}}}},
		{"a self-closing <script/> swallows nothing", "<table><tr><td>a<script/>b</td><td>c</script>d</td></tr></table>", [][][]string{{{"ab", "cd"}}}},
		{"a CDATA section is skipped whole", "<table><tr><td><![CDATA[x<td>y]]>z</td></tr></table>", [][][]string{{{"z"}}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := collectTables(tc.html); !reflect.DeepEqual(got, tc.want) {
				t.Errorf("collectTables(%q)\n got %q\nwant %q", tc.html, got, tc.want)
			}
		})
	}
}
