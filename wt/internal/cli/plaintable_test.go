package cli

import "testing"

// TestPlainTableAlignsBothKindsOfColumn verifies the renderer the usage and
// model listings share: text columns are padded on the right, number columns
// on the left, and no line ends in a space. A table that padded numbers on
// the right would misalign every SIZE and SPEND value.
func TestPlainTableAlignsBothKindsOfColumn(t *testing.T) {
	cols := []PlainColumn{{Head: "KEY"}, {Head: "TEXT"}, {Head: "NUM", Right: true}, {Head: "LAST"}}
	got := RenderPlainTable(cols, [][]string{{"a", "long text", "7", "x"}, {"bb", "t", "1234", ""}}, 0)
	want := "KEY  TEXT        NUM  LAST\n" +
		"a    long text     7  x\n" +
		"bb   t          1234"
	if got != want {
		t.Errorf("table =\n%s\nwant\n%s", got, want)
	}
	if key, rest := PlainTableWidth(cols, [][]string{{"a", "long text", "7", "x"}}); key != 3 || rest != 2+9+2+3+2+4 {
		t.Errorf("PlainTableWidth = %d, %d; want 3 and 22", key, rest)
	}
}
