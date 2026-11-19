package editorkit

import (
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	"github.com/charmbracelet/lipgloss"
	"github.com/ohanaverse/local-ai-setup/wt/internal/themes"
)

// NewTextInput builds a textinput.Model pre-filled with value and the given
// placeholder.
func NewTextInput(value, placeholder string) textinput.Model {
	inp := textinput.New()
	inp.SetValue(value)
	inp.Placeholder = placeholder
	return inp
}

// FormField is one labeled row in a TUI form.
type FormField struct {
	Label string
	Value string
	Focus bool
}

// RenderFormFields renders a list of labeled form fields. The focused row is
// highlighted with the theme's accent color.
func RenderFormFields(theme themes.Theme, fields []FormField) string {
	accent := lipgloss.NewStyle().Foreground(theme.Token(themes.TokenAccent))
	var b strings.Builder
	for _, f := range fields {
		if f.Focus {
			b.WriteString(accent.Render("> " + f.Label + ":"))
		} else {
			b.WriteString("  " + f.Label + ":")
		}
		b.WriteString(" ")
		b.WriteString(f.Value)
		b.WriteString("\n")
	}
	return b.String()
}
