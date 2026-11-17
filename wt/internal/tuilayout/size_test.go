package tuilayout

import "testing"

// TestFormatSize pins the sizes the listing, the Models tab and the start
// warning about other providers' models print: decimal units as `ollama list`
// shows them, and "-" for a size wt does not know, never "0 B".
func TestFormatSize(t *testing.T) {
	for n, want := range map[int64]string{
		0: "-", -1: "-", 7: "7 B", 4_200: "4 kB", 734_000_000: "734 MB", 5_225_388_164: "5.2 GB", 27_000_000_000: "27.0 GB",
		// Just under a unit: the next unit, never "1000" of this one.
		999_700: "1 MB", 999_700_000: "1.0 GB",
	} {
		if got := FormatSize(n); got != want {
			t.Errorf("FormatSize(%d) = %q, want %q", n, got, want)
		}
	}
}
