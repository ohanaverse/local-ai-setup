// wt/internal/profiles/apply_test.go
package profiles

import (
	"os/exec"
	"strings"
	"testing"
)

// TestApplyToCmdEnvWinsOnCollision verifies a profile's Env value for a
// key the command already carries wins — exec.Cmd.Env is documented to
// use the LAST value for a duplicate key, so ApplyToCmd must APPEND
// (never prepend or dedupe) so profile values always land after
// driver-set ones.
func TestApplyToCmdEnvWinsOnCollision(t *testing.T) {
	cmd := exec.Command("true")
	cmd.Env = []string{"FOO=driver-value"}
	rp := ResolvedProfile{Env: map[string]string{"FOO": "profile-value"}}
	if err := ApplyToCmd(cmd, rp); err != nil {
		t.Fatalf("ApplyToCmd() error = %v", err)
	}
	if cmd.Env[len(cmd.Env)-1] != "FOO=profile-value" {
		t.Errorf("cmd.Env = %v, want profile value appended last", cmd.Env)
	}
}

// TestApplyToCmdExtraArgsAppended verifies ExtraArgs land at the end of
// cmd.Args, after whatever BuildLaunchCmd already assembled (driver args,
// user passthrough, resume flag) — see the plan's ordering note in Task 7.
func TestApplyToCmdExtraArgsAppended(t *testing.T) {
	cmd := exec.Command("codex", "--model", "x")
	rp := ResolvedProfile{ExtraArgs: []string{"-c", "model_reasoning_effort=\"low\""}}
	if err := ApplyToCmd(cmd, rp); err != nil {
		t.Fatalf("ApplyToCmd() error = %v", err)
	}
	want := []string{"codex", "--model", "x", "-c", "model_reasoning_effort=\"low\""}
	if strings.Join(cmd.Args, "|") != strings.Join(want, "|") {
		t.Errorf("cmd.Args = %v, want %v", cmd.Args, want)
	}
}

// TestApplyToCmdWrapperReplacesBinaryAndSplicesArgs verifies a Wrapper
// swaps cmd.Path/Args[0] for the wrapper binary and splices the ORIGINAL
// argv (everything after the old argv[0]) into the "{{args}}" template
// slot, dropping the old binary name — the little-coder-wraps-pi case.
func TestApplyToCmdWrapperReplacesBinaryAndSplicesArgs(t *testing.T) {
	cmd := exec.Command("pi", "--model", "ollama/qwen3.8:27b-mlx")
	rp := ResolvedProfile{Wrapper: &WrapperSpec{
		Binary:       "true", // a binary guaranteed to exist on PATH for the test
		ArgsTemplate: []string{"--pi-args", "{{args}}"},
	}}
	if err := ApplyToCmd(cmd, rp); err != nil {
		t.Fatalf("ApplyToCmd() error = %v", err)
	}
	if !strings.HasSuffix(cmd.Path, "true") {
		t.Errorf("cmd.Path = %q, want the resolved `true` binary", cmd.Path)
	}
	want := []string{"--pi-args", "--model", "ollama/qwen3.8:27b-mlx"}
	got := cmd.Args[1:]
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("cmd.Args[1:] = %v, want %v", got, want)
	}
}

// TestApplyToCmdWrapperMissingBinaryErrors verifies a wrapper binary that
// isn't on PATH is a real launch error (not a silent fallback to
// unwrapped) — the user asked for little-coder and it must actually run.
func TestApplyToCmdWrapperMissingBinaryErrors(t *testing.T) {
	cmd := exec.Command("pi")
	rp := ResolvedProfile{Wrapper: &WrapperSpec{Binary: "definitely-not-a-real-binary-xyz"}}
	if err := ApplyToCmd(cmd, rp); err == nil {
		t.Fatal("ApplyToCmd() error = nil, want an error for a missing wrapper binary")
	}
}

// TestApplyToCmdEmptyIsNoop verifies an empty ResolvedProfile changes
// nothing, so calling ApplyToCmd unconditionally (Task 7 does) is always
// safe on the "no profile matched" path.
func TestApplyToCmdEmptyIsNoop(t *testing.T) {
	cmd := exec.Command("pi", "--model", "x")
	before := append([]string{}, cmd.Args...)
	if err := ApplyToCmd(cmd, ResolvedProfile{}); err != nil {
		t.Fatalf("ApplyToCmd() error = %v", err)
	}
	if strings.Join(cmd.Args, "|") != strings.Join(before, "|") {
		t.Errorf("cmd.Args changed on empty ResolvedProfile: %v -> %v", before, cmd.Args)
	}
}

// TestApplyEnvAndArgsNeverAppliesWrapper verifies the split-out
// ApplyEnvAndArgs applies Env/ExtraArgs but leaves Wrapper untouched — a
// caller (cmd/wt's applyResolvedProfile) that wants to interleave a
// config_content mutation between args and wrapper depends on this.
func TestApplyEnvAndArgsNeverAppliesWrapper(t *testing.T) {
	cmd := exec.Command("pi", "--model", "x")
	rp := ResolvedProfile{
		ExtraArgs: []string{"--extra"},
		Wrapper:   &WrapperSpec{Binary: "definitely-not-a-real-binary-xyz"},
	}
	ApplyEnvAndArgs(cmd, rp)
	want := []string{"pi", "--model", "x", "--extra"}
	if strings.Join(cmd.Args, "|") != strings.Join(want, "|") {
		t.Errorf("cmd.Args = %v, want %v (wrapper not applied)", cmd.Args, want)
	}
}

// TestApplyWrapperExported verifies the exported ApplyWrapper (formerly
// unexported applyWrapper) behaves identically — cmd/wt calls it directly
// so it can apply config_content between args and wrapper.
func TestApplyWrapperExported(t *testing.T) {
	cmd := exec.Command("pi", "--model", "x")
	w := &WrapperSpec{Binary: "true", ArgsTemplate: []string{"--wrapped", "{{args}}"}}
	if err := ApplyWrapper(cmd, w); err != nil {
		t.Fatalf("ApplyWrapper() error = %v", err)
	}
	want := []string{"--wrapped", "--model", "x"}
	got := cmd.Args[1:]
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("cmd.Args[1:] = %v, want %v", got, want)
	}
}
