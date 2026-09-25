// wt/internal/profiles/apply.go
package profiles

import (
	"fmt"
	"os/exec"
	"slices"
)

// ApplyEnvAndArgs appends rp.Env (last value wins on a duplicate key,
// matching exec.Cmd.Env's documented behavior) and rp.ExtraArgs (appended
// after everything cmd.Args already carries) to cmd. It never touches
// Wrapper: cmd/wt's applyResolvedProfile calls ApplyConfigContent between
// this and ApplyWrapper, so a codex profile's own declared args land
// before config_content's "--profile agent-wt-profile" flag, and the
// wrapper — applied last — still captures everything appended before it in
// its {{args}} splice.
func ApplyEnvAndArgs(cmd *exec.Cmd, rp ResolvedProfile) {
	for k, v := range rp.Env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	if len(rp.ExtraArgs) > 0 {
		cmd.Args = append(cmd.Args, rp.ExtraArgs...)
	}
}

// ApplyWrapper replaces cmd.Path/Args[0] with w.Binary, splicing the
// command's existing argv (everything after the old argv[0], built so far)
// into the "{{args}}" slot in w.ArgsTemplate. Rejects an ArgsTemplate with
// no "{{args}}" placeholder at all: silently building newArgs from only
// the literal tokens would drop the launched agent's entire original argv
// (e.g. --model, worktree flags) with no error anywhere. Validate also
// rejects this at profiles.toml load time — this check exists too so any
// other caller building a ResolvedProfile by hand (bypassing Validate) is
// still protected.
func ApplyWrapper(cmd *exec.Cmd, w *WrapperSpec) error {
	if !slices.Contains(w.ArgsTemplate, "{{args}}") {
		return fmt.Errorf("profile wrapper %q: args_template %v has no \"{{args}}\" placeholder — the launched agent's own arguments would be silently dropped", w.Binary, w.ArgsTemplate)
	}
	binPath, err := exec.LookPath(w.Binary)
	if err != nil {
		return fmt.Errorf("profile wrapper %q not installed: %w", w.Binary, err)
	}
	original := cmd.Args[1:] // drop the old argv[0] (the pre-wrap binary name)
	var newArgs []string
	for _, tok := range w.ArgsTemplate {
		if tok == "{{args}}" {
			newArgs = append(newArgs, original...)
			continue
		}
		newArgs = append(newArgs, tok)
	}
	cmd.Path = binPath
	cmd.Args = append([]string{binPath}, newArgs...)
	return nil
}

// RestoreAndReattach resets cmd.Env/cmd.Args to a pre-apply snapshot
// (origEnv/origArgs, taken before ApplyEnvAndArgs/ApplyConfigContent ran)
// and reattaches oneShotArgs. Both callers (cmd/wt's applyResolvedProfile,
// wt smoke's realBuildAndRun) use this on the same failure shape: a profile
// application step failed partway through, so the launch must degrade to
// fully unprofiled — never half-profiled — while still producing a runnable
// one-shot command when oneShotArgs is non-empty. Kept as one function so a
// future field added to what must be restored on that degrade is not easy
// to update in one call site and forget in the other.
func RestoreAndReattach(cmd *exec.Cmd, origEnv, origArgs, oneShotArgs []string) {
	cmd.Env = origEnv
	cmd.Args = append(origArgs, oneShotArgs...)
}
