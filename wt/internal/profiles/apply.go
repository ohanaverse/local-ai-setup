// wt/internal/profiles/apply.go
package profiles

import (
	"fmt"
	"os/exec"
)

// ApplyToCmd mutates cmd in place per rp: Env is appended (so it wins on
// a duplicate key, matching exec.Cmd.Env's documented last-value-wins
// behavior), ExtraArgs are appended after everything cmd.Args already
// carries, and a Wrapper — applied last — replaces cmd.Path/Args[0] with
// the wrapper binary, splicing the command's existing argv (built so far,
// including any ExtraArgs just appended) into the "{{args}}" slot in
// ArgsTemplate. A no-op ResolvedProfile changes nothing.
//
// It is the composition of ApplyEnvAndArgs then ApplyWrapper, exported
// separately so a caller combining config_content with args/wrapper (only
// codex declares all three mechanisms together) can interleave a
// config_content-driven cmd.Args mutation between them: env/args first, so
// a codex profile's own declared args land before config_content's
// "--profile agent-wt-profile" flag instead of after it, then wrapper
// last, so its {{args}} splice still captures everything appended before
// it (the one interaction this package's own tests pin).
func ApplyToCmd(cmd *exec.Cmd, rp ResolvedProfile) error {
	if rp.Empty() {
		return nil
	}
	ApplyEnvAndArgs(cmd, rp)
	if rp.Wrapper != nil {
		return ApplyWrapper(cmd, rp.Wrapper)
	}
	return nil
}

// ApplyEnvAndArgs appends rp.Env (last value wins on a duplicate key,
// matching exec.Cmd.Env's documented behavior) and rp.ExtraArgs (appended
// after everything cmd.Args already carries) to cmd. It never touches
// Wrapper — see ApplyToCmd and ApplyWrapper.
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
// into the "{{args}}" slot in w.ArgsTemplate.
func ApplyWrapper(cmd *exec.Cmd, w *WrapperSpec) error {
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
