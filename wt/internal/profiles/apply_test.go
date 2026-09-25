// wt/internal/profiles/apply_test.go
package profiles

import (
	"os/exec"
	"strings"
	"testing"
)

// TestApplyEnvAndArgsEnvWinsOnCollision verifies a profile's Env value for
// a key the command already carries wins — exec.Cmd.Env is documented to
// use the LAST value for a duplicate key, so ApplyEnvAndArgs must APPEND
// (never prepend or dedupe) so profile values always land after
// driver-set ones.
func TestApplyEnvAndArgsEnvWinsOnCollision(t *testing.T) {
	cmd := exec.Command("true")
	cmd.Env = []string{"FOO=driver-value"}
	rp := ResolvedProfile{Env: map[string]string{"FOO": "profile-value"}}
	ApplyEnvAndArgs(cmd, rp)
	if cmd.Env[len(cmd.Env)-1] != "FOO=profile-value" {
		t.Errorf("cmd.Env = %v, want profile value appended last", cmd.Env)
	}
}

// TestApplyEnvAndArgsExtraArgsAppended verifies ExtraArgs land at the end
// of cmd.Args, after whatever BuildLaunchCmd already assembled (driver
// args, user passthrough, resume flag) — matching cmd/wt's
// applyResolvedProfile ordering (env/args before config_content/wrapper).
func TestApplyEnvAndArgsExtraArgsAppended(t *testing.T) {
	cmd := exec.Command("codex", "--model", "x")
	rp := ResolvedProfile{ExtraArgs: []string{"-c", "model_reasoning_effort=\"low\""}}
	ApplyEnvAndArgs(cmd, rp)
	want := []string{"codex", "--model", "x", "-c", "model_reasoning_effort=\"low\""}
	if strings.Join(cmd.Args, "|") != strings.Join(want, "|") {
		t.Errorf("cmd.Args = %v, want %v", cmd.Args, want)
	}
}

// TestRestoreAndReattachDropsMutationsAndKeepsOneShotArgs verifies
// RestoreAndReattach's two call sites (cmd/wt's applyResolvedProfile, wt
// smoke's realBuildAndRun) both get a genuinely clean revert: any
// cmd.Env/cmd.Args mutation made between the snapshot and the failure must
// be gone afterward, not just have oneShotArgs appended on top of it — a
// half-reverted degrade would leave the "profiles disabled for this launch"
// message a lie.
func TestRestoreAndReattachDropsMutationsAndKeepsOneShotArgs(t *testing.T) {
	cmd := exec.Command("codex", "--model", "x")
	origEnv := append([]string{}, cmd.Env...)
	origArgs := append([]string{}, cmd.Args...)
	cmd.Env = append(cmd.Env, "PROFILE_VAR=1")
	cmd.Args = append(cmd.Args, "-c", "model_provider=agent-wt")

	RestoreAndReattach(cmd, origEnv, origArgs, []string{"exec", "the prompt"})

	for _, e := range cmd.Env {
		if e == "PROFILE_VAR=1" {
			t.Errorf("cmd.Env = %v, still carries the profile's env mutation after revert", cmd.Env)
		}
	}
	want := []string{"codex", "--model", "x", "exec", "the prompt"}
	if strings.Join(cmd.Args, "|") != strings.Join(want, "|") {
		t.Errorf("cmd.Args = %v, want %v (profile args dropped, oneShotArgs reattached)", cmd.Args, want)
	}
}

// TestApplyEnvAndArgsThenApplyWrapperComposesCorrectly verifies the real
// production sequence (cmd/wt's applyResolvedProfile: ApplyEnvAndArgs,
// then — with a config_content step interleaved in production, omitted
// here since it's tested separately — ApplyWrapper) swaps cmd.Path/Args[0]
// for the wrapper binary and splices the ORIGINAL argv, including
// anything ApplyEnvAndArgs already appended, into the "{{args}}" template
// slot — the little-coder-wraps-pi case.
func TestApplyEnvAndArgsThenApplyWrapperComposesCorrectly(t *testing.T) {
	cmd := exec.Command("pi", "--model", "ollama/qwen3.8:27b-mlx")
	rp := ResolvedProfile{
		ExtraArgs: []string{"--extra"},
		Wrapper: &WrapperSpec{
			Binary:       "true", // a binary guaranteed to exist on PATH for the test
			ArgsTemplate: []string{"--pi-args", "{{args}}"},
		},
	}
	ApplyEnvAndArgs(cmd, rp)
	if err := ApplyWrapper(cmd, rp.Wrapper); err != nil {
		t.Fatalf("ApplyWrapper() error = %v", err)
	}
	if !strings.HasSuffix(cmd.Path, "true") {
		t.Errorf("cmd.Path = %q, want the resolved `true` binary", cmd.Path)
	}
	want := []string{"--pi-args", "--model", "ollama/qwen3.8:27b-mlx", "--extra"}
	got := cmd.Args[1:]
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("cmd.Args[1:] = %v, want %v", got, want)
	}
}

// TestApplyWrapperMissingBinaryErrors verifies a wrapper binary that isn't
// on PATH is a real launch error (not a silent fallback to unwrapped) —
// the user asked for little-coder and it must actually run.
func TestApplyWrapperMissingBinaryErrors(t *testing.T) {
	cmd := exec.Command("pi")
	w := &WrapperSpec{Binary: "definitely-not-a-real-binary-xyz", ArgsTemplate: []string{"{{args}}"}}
	if err := ApplyWrapper(cmd, w); err == nil {
		t.Fatal("ApplyWrapper() error = nil, want an error for a missing wrapper binary")
	}
}

// TestApplyWrapperErrorsWhenArgsTemplateMissingPlaceholder is the
// regression lock for the code-review finding that a wrapper profile's
// ArgsTemplate omitting the "{{args}}" placeholder silently dropped the
// launched agent's entire original argv (e.g. --model, worktree flags)
// instead of failing loudly.
func TestApplyWrapperErrorsWhenArgsTemplateMissingPlaceholder(t *testing.T) {
	cmd := exec.Command("pi", "--model", "x")
	w := &WrapperSpec{Binary: "true", ArgsTemplate: []string{"--wrapped-with-no-placeholder"}}
	if err := ApplyWrapper(cmd, w); err == nil {
		t.Fatal("ApplyWrapper() error = nil, want an error for an args_template with no \"{{args}}\" placeholder")
	}
}

// TestApplyEnvAndArgsNeverAppliesWrapper verifies ApplyEnvAndArgs applies
// Env/ExtraArgs but leaves Wrapper untouched — cmd/wt's applyResolvedProfile
// depends on this to interleave a config_content mutation between the two.
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

// TestApplyWrapperExported verifies ApplyWrapper behaves as documented —
// cmd/wt calls it directly so it can apply config_content between args and
// wrapper.
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
