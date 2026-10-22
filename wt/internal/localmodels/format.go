package localmodels

import "fmt"

// FormatSize renders a byte count the way `ollama list` does: decimal units,
// one decimal place from a gigabyte up. 0 (not known) is "-". A unit starts
// where the one below it would round to 1000, so no size prints as "1000 MB".
// `wt model list`, the Models tab of `wt config` and the start engine's
// figures for other providers' models all print sizes through it.
func FormatSize(n int64) string {
	switch {
	case n <= 0:
		return "-"
	case n >= 999_500_000:
		return fmt.Sprintf("%.1f GB", float64(n)/1e9)
	case n >= 999_500:
		return fmt.Sprintf("%.0f MB", float64(n)/1e6)
	case n >= 1_000:
		return fmt.Sprintf("%.0f kB", float64(n)/1e3)
	}
	return fmt.Sprintf("%d B", n)
}
