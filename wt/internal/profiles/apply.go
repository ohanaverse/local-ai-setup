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
func ApplyToCmd(cmd *exec.Cmd, rp ResolvedProfile) error {
	if rp.Empty() {
		return nil
	}
	for k, v := range rp.Env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	if len(rp.ExtraArgs) > 0 {
		cmd.Args = append(cmd.Args, rp.ExtraArgs...)
	}
	if rp.Wrapper != nil {
		if err := applyWrapper(cmd, rp.Wrapper); err != nil {
			return err
		}
	}
	return nil
}

func applyWrapper(cmd *exec.Cmd, w *WrapperSpec) error {
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
