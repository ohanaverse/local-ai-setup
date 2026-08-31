package configeditor

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/ohanaverse/local-ai-setup/wt/internal/themes"
)

// formField is one labeled row in a TUI form.
type formField struct {
	label string
	value string
	focus bool
}

// renderFormFields renders a list of labeled form fields. The focused row is
// highlighted with the theme's accent color.
func renderFormFields(theme themes.Theme, fields []formField) string {
	accent := lipgloss.NewStyle().Foreground(theme.Token(themes.TokenAccent))
	var b strings.Builder
	for _, f := range fields {
		if f.focus {
			b.WriteString(accent.Render("> " + f.label + ":"))
		} else {
			b.WriteString("  " + f.label + ":")
		}
		b.WriteString(" ")
		b.WriteString(f.value)
		b.WriteString("\n")
	}
	return b.String()
}
