package agents

import (
	"slices"
	"testing"
)

// TestOneShotArgs asserts each one-shot-capable driver returns the exact
// non-interactive invocation flags wt smoke depends on to run a single
// prompt and exit — a wrong flag here silently breaks wt smoke for that
// agent without any test ever executing a real binary.
func TestOneShotArgs(t *testing.T) {
	tests := []struct {
		agent string
		want  []string
	}{
		{"claude", []string{"-p", "hello"}},
		{"codex", []string{"exec", "hello"}},
		{"copilot", []string{"-p", "hello"}},
		{"opencode", []string{"run", "hello"}},
		{"pi", []string{"-p", "hello"}},
		{"agy", []string{"-p", "hello"}},
	}
	for _, tt := range tests {
		t.Run(tt.agent, func(t *testing.T) {
			d := ByName(tt.agent)
			if d == nil {
				t.Fatalf("unknown agent %q", tt.agent)
			}
			osr, ok := d.(OneShotRunner)
			if !ok {
				t.Fatalf("driver %q does not implement OneShotRunner", tt.agent)
			}
			got := osr.OneShotArgs("hello")
			if !slices.Equal(got, tt.want) {
				t.Fatalf("OneShotArgs = %v, want %v", got, tt.want)
			}
		})
	}
}

// TestShellNotOneShotRunner asserts shell — a command with no model layer —
// does not implement OneShotRunner. wt smoke's eligibility filter relies on
// agents.IsCommand to exclude shell, not on a missing method; this test
// guards against the two mechanisms silently diverging.
func TestShellNotOneShotRunner(t *testing.T) {
	d := ByName("shell")
	if d == nil {
		t.Fatal(`unknown agent "shell"`)
	}
	if _, ok := d.(OneShotRunner); ok {
		t.Fatal("shellDriver unexpectedly implements OneShotRunner")
	}
}
