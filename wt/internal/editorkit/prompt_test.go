package editorkit

import (
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

// The question must be the last thing before the hints.
func TestPromptViewKeepsTheQuestionAboveTheHints(t *testing.T) {
	plain := lipgloss.NewStyle()
	got := ansi.Strip(PromptView("BAR", "some text", "Apply? [y/N]", []string{"y apply · n cancel"}, 40, 10, plain))
	want := "BAR\n\nsome text\n\nApply? [y/N]\ny apply · n cancel"
	if got != want {
		t.Fatalf("PromptView =\n%q\nwant\n%q", got, want)
	}
}

// A plan step too wide for the terminal continues under its own text, not
// under the step number, and the question still comes last.
func TestPlanPromptViewHangsTheSteps(t *testing.T) {
	plain := lipgloss.NewStyle()
	v := ansi.Strip(PlanPromptView("BAR", "plan:\n  1. aaa bbb ccc ddd eee fff ggg hhh iii jjj\n", "Apply? [y/N]", []string{"y"}, 24, 20, plain))
	if !strings.Contains(v, "\n     eee fff ggg hhh iii\n") {
		t.Errorf("PlanPromptView does not hang the step's continuation:\n%s", v)
	}
	if !strings.HasSuffix(v, "Apply? [y/N]\ny") {
		t.Errorf("PlanPromptView does not end with the question and the hints:\n%q", v)
	}
}
