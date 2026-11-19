package editorkit

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/ohanaverse/local-ai-setup/wt/internal/tuilayout"
)

// PromptView is a confirm phase: what the question is about, then the
// question on the line above the hints. A text too tall for the terminal
// loses lines from its middle (L4); the question is last, so it stays.
func PromptView(bar, text, question string, hints []string, width, height int, dim lipgloss.Style) string {
	body := "\n" + WrapText(text, width) + "\n\n" + WrapText(question, width)
	return tuilayout.MessageAlone(bar, body, FitHints(width, hints), width, height, dim)
}

// PlanPromptView is PromptView for a plan: its steps wrap with their
// continuation lines hung under the step's text.
func PlanPromptView(bar, planText, question string, hints []string, width, height int, dim lipgloss.Style) string {
	body := "\n" + WrapIndented(strings.TrimRight(planText, "\n"), width) + "\n\n" + WrapText(question, width)
	return tuilayout.MessageAlone(bar, body, FitHints(width, hints), width, height, dim)
}
