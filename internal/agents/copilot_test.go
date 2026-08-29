package agents

import (
	"testing"
)

// TestCopilotSeeder asserts copilotDriver returns the Copilot instruction
// pointer file.
func TestCopilotSeeder(t *testing.T) {
	var d Driver = copilotDriver{}
	s, ok := d.(Seeder)
	if !ok {
		t.Fatal("copilotDriver does not implement Seeder")
	}
	ptrs := s.InstructionPointers()
	if len(ptrs) != 1 {
		t.Fatalf("expected 1 pointer, got %d", len(ptrs))
	}
	want := InstructionPointer{
		Path:    ".github/copilot-instructions.md",
		Content: "Read AGENTS.md and follow all instructions in it.\n",
	}
	if ptrs[0] != want {
		t.Errorf("pointer = %+v, want %+v", ptrs[0], want)
	}
}

// TestCopilotOllamaURL asserts copilotDriver returns the /v1 endpoint.
func TestCopilotOllamaURL(t *testing.T) {
	var d Driver = copilotDriver{}
	u, ok := d.(OllamaURLer)
	if !ok {
		t.Fatal("copilotDriver does not implement OllamaURLer")
	}
	if got := u.OllamaURL(); got != "http://localhost:11434/v1" {
		t.Errorf("OllamaURL() = %q, want http://localhost:11434/v1", got)
	}
}
