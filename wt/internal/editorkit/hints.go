package editorkit

import (
	"unicode/utf8"

	"github.com/ohanaverse/local-ai-setup/wt/internal/tuilayout"
)

// FitHints is the fullest of hints that fits width; the last one, cut, when
// none does.
func FitHints(width int, hints []string) string {
	for _, h := range hints {
		if width <= 0 || utf8.RuneCountInString(h) <= width {
			return h
		}
	}
	return tuilayout.Clip(hints[len(hints)-1], width)
}
