package editorkit

import (
	"testing"

	"github.com/charmbracelet/x/ansi"
	"github.com/ohanaverse/local-ai-setup/wt/internal/themes"
)

// TestNewTextInputKeepsValueAndPlaceholder verifies a new input holds the
// value and placeholder it was given and is not focused: every form opens
// pre-filled and unfocused until its cursor reaches the field.
func TestNewTextInputKeepsValueAndPlaceholder(t *testing.T) {
	in := NewTextInput("v", "p")
	if got := in.Value(); got != "v" {
		t.Errorf("Value() = %q, want %q", got, "v")
	}
	if in.Placeholder != "p" {
		t.Errorf("Placeholder = %q, want %q", in.Placeholder, "p")
	}
	if in.Focused() {
		t.Error("Focused() = true, want false")
	}
}

// TestRenderFormFieldsMarksTheFocusedRow verifies the focused row carries the
// "> " marker and the others an indent: the focused row must read as focused
// without colour.
func TestRenderFormFieldsMarksTheFocusedRow(t *testing.T) {
	got := ansi.Strip(RenderFormFields(themes.Theme{}, []FormField{
		{Label: "Name", Value: "a", Focus: true},
		{Label: "URL", Value: "b"},
	}))
	if want := "> Name: a\n  URL: b\n"; got != want {
		t.Errorf("RenderFormFields = %q, want %q", got, want)
	}
}
